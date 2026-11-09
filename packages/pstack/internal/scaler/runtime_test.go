package scaler

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestEnsureTofu(t *testing.T) {
	// negative control: skip the sha256 comparison in EnsureTofu → the wrong-sum case installs a binary and this fails
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("tofu")
	_, _ = w.Write([]byte("#!/bin/sh\necho tofu\n"))
	_ = zw.Close()
	sum := sha256.Sum256(zb.Bytes())

	var hits atomic.Int32
	want := "/v" + TofuVersion + "/tofu_" + TofuVersion + "_linux_" + runtime.GOARCH + ".zip"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != want {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(zb.Bytes())
	}))
	defer srv.Close()
	oldBase, oldSum, hadSum := tofuBase, tofuSums[runtime.GOARCH], tofuSums[runtime.GOARCH] != ""
	t.Cleanup(func() {
		tofuBase = oldBase
		if hadSum {
			tofuSums[runtime.GOARCH] = oldSum
		} else {
			delete(tofuSums, runtime.GOARCH)
		}
	})
	tofuBase = srv.URL

	// A wrong sum: refused, nothing left behind.
	bad := t.TempDir()
	tofuSums[runtime.GOARCH] = strings.Repeat("0", 64)
	if _, err := EnsureTofu(bad, srv.Client()); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("wrong sum: err = %v", err)
	}
	if left, _ := os.ReadDir(bad); len(left) != 0 {
		t.Fatalf("wrong sum left %v", left)
	}

	dir := t.TempDir()
	tofuSums[runtime.GOARCH] = hex.EncodeToString(sum[:])
	path, err := EnsureTofu(dir, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "tofu-"+TofuVersion) {
		t.Errorf("path = %s", path)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("stat = %v, %v", fi, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "#!/bin/sh\necho tofu\n" {
		t.Errorf("content = %q", b)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Errorf("dir holds %d entries, want only the binary", len(left))
	}
	before := hits.Load()
	if again, err := EnsureTofu(dir, srv.Client()); err != nil || again != path {
		t.Fatalf("second call = %s, %v", again, err)
	}
	if hits.Load() != before {
		t.Error("second call downloaded again")
	}

	// An architecture with no pinned sum is refused.
	delete(tofuSums, runtime.GOARCH)
	if _, err := EnsureTofu(t.TempDir(), srv.Client()); err == nil {
		t.Error("an unpinned arch was accepted")
	}
}

func TestTofuEnv(t *testing.T) {
	// negative control: build cmd.Env from os.Environ() in Tofu.Run → PSTACK_TOKEN reaches env.log and this fails
	t.Setenv("PSTACK_TOKEN", "x-root-token-value")
	bin, dir := fakeTofu(t)
	state := t.TempDir()
	work := filepath.Join(state, "tofu", "hetzner")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	tf := Tofu{Bin: bin, Dir: work, StateDir: state, Creds: map[string]string{"HCLOUD_TOKEN": "hc-secret-123"}}
	if _, err := tf.Run("state", "list"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "env.log"))
	env := string(b)
	for _, want := range []string{
		"HCLOUD_TOKEN=hc-secret-123", "HOME=" + state, "TF_IN_AUTOMATION=1", "TF_INPUT=0",
		"TF_PLUGIN_CACHE_DIR=" + filepath.Join(state, "plugins"), "PATH=",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env.log lacks %s", want)
		}
	}
	if strings.Contains(env, "PSTACK_TOKEN") {
		t.Error("env.log has PSTACK_TOKEN")
	}
	if fi, err := os.Stat(filepath.Join(state, "plugins")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("plugins dir = %v, %v", fi, err)
	}
	if calls, _ := os.ReadFile(filepath.Join(dir, "calls.log")); string(calls) != "state list\n" {
		t.Errorf("calls.log = %q", calls)
	}
}

func TestTofuRunErrorIsScrubbedTail(t *testing.T) {
	// negative control: return the raw output as the error in Tofu.Run → the credential appears and this fails
	bin, dir := fakeTofu(t)
	state := t.TempDir()
	var lines []string
	for i := range 30 {
		lines = append(lines, "line "+string(rune('a'+i%26)))
	}
	lines = append(lines, "Error: token hc-secret-123 refused")
	_ = os.WriteFile(filepath.Join(dir, "fail-validate"), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	tf := Tofu{Bin: bin, Dir: state, StateDir: state, Creds: map[string]string{"HCLOUD_TOKEN": "hc-secret-123"}}
	out, err := tf.Run("validate")
	if err == nil {
		t.Fatal("fail-validate did not fail")
	}
	if strings.Contains(out, "hc-secret-123") || strings.Contains(err.Error(), "hc-secret-123") {
		t.Errorf("credential leaked: %q / %q", out, err)
	}
	if n := len(strings.Split(err.Error(), "\n")); n != 20 {
		t.Errorf("error has %d lines, want 20", n)
	}
	if !strings.HasSuffix(err.Error(), "refused") {
		t.Errorf("error tail = %q", err)
	}
}

func TestFakeTofuState(t *testing.T) {
	// negative control: make the fake's refresh ignore <dir>/deleted → pstack-aaaaaa stays listed and this fails
	bin, dir := fakeTofu(t)
	work := t.TempDir()
	tf := Tofu{Bin: bin, Dir: work, StateDir: t.TempDir()}
	if err := WriteTFVars(work, map[string]any{"workers": map[string]any{"pstack-aaaaaa": map[string]string{}, "pstack-bbbbbb": map[string]string{}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := tf.Run("apply", "-auto-approve"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "deleted"), []byte("pstack-aaaaaa\n"), 0o600)
	if _, err := tf.Run("apply", "-refresh-only", "-auto-approve"); err != nil {
		t.Fatal(err)
	}
	out, err := tf.Run("state", "list")
	if err != nil || out != "hcloud_server.worker[\"pstack-bbbbbb\"]\n" {
		t.Fatalf("state list = %q, %v", out, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "fail"), []byte("boom\n"), 0o600)
	if _, err := tf.Run("apply", "-auto-approve"); err == nil || err.Error() != "boom" {
		t.Errorf("fail: err = %v", err)
	}
	if _, err := tf.Run("destroy", "-auto-approve"); err != nil {
		t.Fatal(err)
	}
	if out, _ := tf.Run("state", "list"); out != "" {
		t.Errorf("after destroy: %q", out)
	}
}

func TestTFVarsAndStateAre0600(t *testing.T) {
	// negative control: write straight to the target in writeAtomic → the read-only dir case truncates the target and this fails
	dir := t.TempDir()
	tfvars := filepath.Join(dir, "workers.auto.tfvars.json")
	// A stale .next from a crash, at a wider mode.
	if err := os.WriteFile(tfvars+".next", []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteTFVars(dir, map[string]any{"workers": map[string]any{"pstack-aaaaaa": map[string]string{"user_data": "x"}}}); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{tfvars, filepath.Join(dir, "scaler.json")} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", p, fi, err)
		}
	}
	if _, err := os.Stat(tfvars + ".next"); !os.IsNotExist(err) {
		t.Error("stale .next left behind")
	}
	var v map[string]map[string]any
	if b, _ := os.ReadFile(tfvars); json.Unmarshal(b, &v) != nil || v["workers"]["pstack-aaaaaa"] == nil {
		t.Errorf("tfvars = %v", v)
	}

	if os.Geteuid() == 0 {
		t.Skip("root ignores a read-only dir")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := WriteTFVars(dir, map[string]any{"workers": map[string]any{}}); err == nil {
		t.Error("write into a read-only dir succeeded")
	}
	s.LastPreset = "aws"
	if err := s.Save(dir); err == nil {
		t.Error("save into a read-only dir succeeded")
	}
	if b, _ := os.ReadFile(tfvars); json.Unmarshal(b, &v) != nil || v["workers"]["pstack-aaaaaa"] == nil {
		t.Errorf("tfvars after a failed write = %q", b)
	}
	again, err := LoadState(dir)
	if err != nil || again.HostID != s.HostID || again.LastPreset != "" {
		t.Errorf("state after a failed write = %+v, %v", again, err)
	}
}

func TestLoadStateMakesHostID(t *testing.T) {
	// negative control: skip the Save in LoadState's absent branch → the second load mints a new id and this fails
	dir := t.TempDir()
	s, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(s.HostID) {
		t.Fatalf("HostID = %q", s.HostID)
	}
	again, err := LoadState(dir)
	if err != nil || again.HostID != s.HostID {
		t.Errorf("second load = %+v, %v", again, err)
	}
	s.Workers = []OwnedWorker{{Name: "pstack-abcdef", Vars: map[string]string{"server_type": "cx22"}, AppliedAt: 5}}
	s.Locked = &Locked{Preset: "hetzner", FilesHash: "h", Shared: map[string]string{"ssh_keys": "[]"}}
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	third, _ := LoadState(dir)
	if len(third.Workers) != 1 || third.Workers[0].Vars["server_type"] != "cx22" || third.Locked.Preset != "hetzner" {
		t.Errorf("round trip = %+v", third)
	}
}

func TestLockIsExclusive(t *testing.T) {
	// negative control: take LOCK_SH instead of LOCK_EX in Lock → the second Lock succeeds and this fails
	dir := t.TempDir()
	release, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(dir); err == nil || err.Error() != "another pstack scaler holds "+dir {
		t.Fatalf("second Lock: %v", err)
	}
	release()
	again, err := Lock(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

func TestWithHostname(t *testing.T) {
	// negative control: insert the keys before the #cloud-config line → the result is not cloud-config and this fails
	b, err := os.ReadFile("../../../conformance/golden/cli/swarm-join-cloud-config.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct{ Stdout string }
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	out, err := WithHostname(golden.Stdout, "pstack-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Errorf("first line lost: %q", out[:40])
	}
	var before, after map[string]any
	if err := yaml.Unmarshal([]byte(golden.Stdout), &before); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(out), &after); err != nil {
		t.Fatalf("result is not YAML: %v", err)
	}
	if after["hostname"] != "pstack-abc123" || after["preserve_hostname"] != false {
		t.Errorf("hostname = %v, preserve_hostname = %v", after["hostname"], after["preserve_hostname"])
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			t.Errorf("key %s lost", k)
		}
	}
	if len(after) != len(before)+2 {
		t.Errorf("keys %d, want %d", len(after), len(before)+2)
	}
	if _, err := WithHostname(out, "pstack-other"); err == nil {
		t.Error("a text already holding hostname: was accepted")
	}
	if _, err := WithHostname("#!/bin/sh\necho\n", "pstack-abc123"); err == nil {
		t.Error("a non-cloud-config text was accepted")
	}
}

func TestScrub(t *testing.T) {
	// negative control: skip the JSON-escaped form in Scrub's leaf walk → the escaped private key survives and this fails
	key := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkq\n-----END PRIVATE KEY-----\n"
	gcp, _ := json.Marshal(map[string]any{"type": "service_account", "private_key": key, "short": "7chars!", "nested": map[string]any{"x": []any{"nested-leaf-1234"}}})
	escaped, _ := json.Marshal(key)
	creds := map[string]string{"HCLOUD_TOKEN": "hc-secret-123", "GOOGLE_CREDENTIALS": string(gcp)}
	text := "token hc-secret-123\n" +
		"decoded: " + key +
		"escaped: " + string(escaped[1:len(escaped)-1]) + "\n" +
		"nested nested-leaf-1234 short 7chars!\n" +
		"join SWMTKN-1-abc-def\n"
	got := Scrub(text, creds)
	for _, gone := range []string{"hc-secret-123", "MIIEvQIBADANBgkq", "nested-leaf-1234", "abc-def"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "7chars!") {
		t.Error("a 7-char leaf was scrubbed")
	}

	// Scrub whole, then tail: a secret spanning the cut is still gone.
	long := "a\nb\n" + key + "end\n"
	tail := Tail(Scrub(long, creds), 2)
	if strings.Contains(tail, "END PRIVATE KEY") {
		t.Errorf("secret across the cut survived: %q", tail)
	}
	if got := Tail("1\n2\n3\n4\n", 2); got != "3\n4" {
		t.Errorf("Tail = %q", got)
	}
	if got := Tail("1\n2", 5); got != "1\n2" {
		t.Errorf("Tail short = %q", got)
	}
}

func TestClientDo(t *testing.T) {
	// negative control: json-decode into a *string out in Client.Do → the text/plain join answer fails to decode and this fails
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer root-tok" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unauthenticated"}`))
			return
		}
		switch r.URL.Path {
		case "/api/swarm/join":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("#cloud-config\nruncmd: []\n"))
		case "/api/scaler/report":
			var rep Report
			if json.NewDecoder(r.Body).Decode(&rep) != nil || rep.HostID != "abcd1234" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"bad report"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(503)
			_, _ = w.Write([]byte("not json"))
		}
	}))
	defer srv.Close()
	c := Client{Base: srv.URL, Token: "root-tok", HTTP: srv.Client()}
	var text string
	if err := c.Do("GET", "/api/swarm/join?format=cloud-config", nil, &text); err != nil || text != "#cloud-config\nruncmd: []\n" {
		t.Errorf("join = %q, %v", text, err)
	}
	var ok struct{ OK bool }
	if err := c.Do("POST", "/api/scaler/report", Report{HostID: "abcd1234"}, &ok); err != nil || !ok.OK {
		t.Errorf("report = %v, %v", ok, err)
	}
	if err := c.Do("POST", "/api/scaler/report", Report{}, nil); err == nil || err.Error() != "bad report" {
		t.Errorf("400 err = %v", err)
	}
	if err := (Client{Base: srv.URL, HTTP: srv.Client()}).Do("GET", "/api/scaler/config", nil, nil); err == nil || err.Error() != "unauthenticated" {
		t.Errorf("401 err = %v", err)
	}
	if err := c.Do("GET", "/other", nil, nil); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("503 err = %v", err)
	}
}
