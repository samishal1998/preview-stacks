package routing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mint self-signs a certificate for the names, valid over [from, until].
func mint(t *testing.T, names []string, from, until time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    from,
		NotAfter:     until,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

func TestTheOrderOfWritesIsPEMsThenPointer(t *testing.T) {
	// negative control: hoist `s.write(WildcardYAML, …)` above the PEM loop in SetWildcard — the
	// pointer then survives a failed pair write, and Traefik's rebuild (which the pointer IS)
	// inlines certificate files that do not exist, dropping the entry with only a log line.
	//
	// Proven by making the pair write FAIL and asserting nothing points at it: an end-state test
	// cannot see ordering, because both orders leave the same three files on success.
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode this test needs")
	}
	s := New(t.TempDir())
	now := time.Now()
	cert, key := mint(t, []string{"*.preview.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	certs := filepath.Join(s.Dir, "certs")
	if err := os.MkdirAll(certs, 0o500); err != nil { // readable, NOT writable
		t.Fatal(err)
	}
	if _, err := s.SetWildcard(cert, key, nil); err == nil {
		t.Fatal("the pair write must fail on an unwritable certs directory")
	}
	if s.WildcardActive() {
		t.Fatal("a failed pair write must leave NO pointer — Traefik would inline files that are not there")
	}
	if _, err := s.Read(WildcardYAML); err == nil {
		t.Fatal("the pointer file must not exist")
	}

	// And a first store whose POINTER write fails takes the key back out with it: the API would
	// otherwise deny holding a private key that is sitting in Traefik's directory.
	if err := os.Chmod(certs, 0o700); err != nil {
		t.Fatal(err)
	}
	// A directory where the pointer cannot be created, but certs/ can: make the pointer name a
	// directory, so the rename onto it fails.
	if err := os.MkdirAll(filepath.Join(s.Dir, WildcardYAML), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetWildcard(cert, key, nil); err == nil {
		t.Fatal("the pointer write must fail")
	}
	for _, rel := range []string{"certs/wildcard.crt", "certs/wildcard.key"} {
		if _, err := os.Stat(filepath.Join(s.Dir, rel)); !os.IsNotExist(err) {
			t.Errorf("a first store that could not be pointed at must not leave %s behind", rel)
		}
	}
}

func TestARemoveSweepsAKeyLeftWithoutItsPointer(t *testing.T) {
	// negative control: restore the `if !s.WildcardActive() { return false, nil }` early return in
	// RemoveWildcard — a key orphaned by a crash between the two unlinks can never be deleted
	// through the API again, while every DELETE answers "no wildcard is stored".
	s := New(t.TempDir())
	now := time.Now()
	cert, key := mint(t, []string{"*.preview.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := s.SetWildcard(cert, key, nil); err != nil {
		t.Fatal(err)
	}
	// The crash: the pointer went, the process died before the pair did.
	if err := os.Remove(filepath.Join(s.Dir, WildcardYAML)); err != nil {
		t.Fatal(err)
	}
	removed, err := s.RemoveWildcard()
	if err != nil || !removed {
		t.Fatalf("the orphaned pair must be removable and reported: %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "certs/wildcard.key")); !os.IsNotExist(err) {
		t.Error("the key must be gone")
	}
	// Nothing left: now it is honestly "nothing to remove".
	if removed, err := s.RemoveWildcard(); removed || err != nil {
		t.Errorf("an empty store removes nothing: %v %v", removed, err)
	}
}

func TestWildcardStoreValidatesAndStoresThePair(t *testing.T) {
	// negative control: drop the tls.X509KeyPair validation — the rotation below could store a
	// mismatched pair and the info would still read back, which is the whole point of validating.
	s := New(t.TempDir())
	good := time.Now().Add(-time.Hour)
	cert, key := mint(t, []string{"*.preview.example.com", "preview.example.com"}, good, good.Add(90*24*time.Hour))

	if s.WildcardActive() || s.WildcardInfo() != nil {
		t.Fatal("empty store must be inactive")
	}

	info, err := s.SetWildcard(cert, key, []string{"preview.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.WildcardActive() || info == nil || info.Domains[0] != "*.preview.example.com" || !info.SelfSigned {
		t.Fatalf("info: %+v", info)
	}
	yaml, err := s.Read(WildcardYAML)
	if err != nil {
		t.Fatal(err)
	}
	// The YAML names the paths TRAEFIK sees (its mount), never this process's directory.
	if !strings.Contains(yaml, "certFile: /etc/traefik/dynamic/certs/wildcard.crt") || !strings.Contains(yaml, "keyFile: /etc/traefik/dynamic/certs/wildcard.key") {
		t.Fatalf("yaml: %s", yaml)
	}
	if st, err := os.Stat(filepath.Join(s.Dir, "certs/wildcard.key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the key is a secret: %v %v", st, err)
	}

	// Rotation: a second PUT replaces the pair, and the info reads the NEW certificate.
	cert2, key2 := mint(t, []string{"*.preview.example.com"}, good, good.Add(300*24*time.Hour))
	info2, err := s.SetWildcard(cert2, key2, []string{"preview.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if info2.NotAfter <= info.NotAfter {
		t.Fatal("rotation must store the new certificate")
	}

	removed, err := s.RemoveWildcard()
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if s.WildcardActive() {
		t.Fatal("still active after remove")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "certs/wildcard.key")); !os.IsNotExist(err) {
		t.Fatal("the key must not outlive the mode")
	}
	if again, _ := s.RemoveWildcard(); again {
		t.Fatal("a second remove has nothing to remove")
	}
}

func TestThePointerIsNotAnOperatorFile(t *testing.T) {
	// negative control: drop assertNotReserved from Write/Remove — the generic routing API (which
	// is MAINTAINER's) can then create or delete the file whose presence IS the mode, walking
	// around the admin gate on /api/tls/wildcard and orphaning the key pair on the way out.
	s := New(t.TempDir())
	if _, err := s.Write(WildcardYAML, "tls:\n  certificates: []\n"); err == nil || !strings.Contains(err.Error(), "managed by pstack") {
		t.Errorf("writing the pointer by hand must be refused: %v", err)
	}
	// The wildcard's own path still writes it — the gate is on the generic door, not the owner.
	now := time.Now()
	cert, key := mint(t, []string{"*.preview.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := s.SetWildcard(cert, key, []string{"preview.example.com"}); err != nil {
		t.Fatalf("the owner must still write it: %v", err)
	}
	if _, err := s.Remove(WildcardYAML); err == nil || !strings.Contains(err.Error(), "managed by pstack") {
		t.Errorf("deleting the pointer by hand must be refused: %v", err)
	}
	// And it is still LISTED: hiding it would be worse than owning it — an operator must see the
	// file that is in the directory.
	found := false
	for _, f := range s.List() {
		if f.Name == WildcardYAML {
			found = true
		}
	}
	if !found {
		t.Error("the pointer must stay visible in the listing")
	}
	if ok, err := s.RemoveWildcard(); !ok || err != nil {
		t.Errorf("the owner must still remove it: %v %v", ok, err)
	}
}

func TestWildcardRefusals(t *testing.T) {
	// negative control: drop the tls.X509KeyPair check — a cert stored with someone else's key
	// passes, and every preview serves a handshake failure that Traefik reports only in its logs.
	s := New(t.TempDir())
	now := time.Now()
	cert, _ := mint(t, []string{"*.preview.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	_, otherKey := mint(t, []string{"*.preview.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))

	if _, err := s.SetWildcard(cert, otherKey, nil); err == nil || !strings.Contains(err.Error(), "do not form a usable pair") {
		t.Errorf("mismatch: %v", err)
	}
	expiredCert, expiredKey := mint(t, []string{"*.preview.example.com"}, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	if _, err := s.SetWildcard(expiredCert, expiredKey, nil); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired: %v", err)
	}
	wrongCert, wrongKey := mint(t, []string{"*.other.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	if _, err := s.SetWildcard(wrongCert, wrongKey, []string{"preview.example.com"}); err == nil || !strings.Contains(err.Error(), "does not cover *.preview.example.com") {
		t.Errorf("wrong zone: %v", err)
	}
	if _, err := s.SetWildcard("not pem", "not pem", nil); err == nil {
		t.Error("garbage must be refused")
	}
	// No zone check when the host has no domain configured — a lab host still gets to store one.
	if _, err := s.SetWildcard(wrongCert, wrongKey, nil); err != nil {
		t.Errorf("no domain, no zone check: %v", err)
	}
}

func TestDynamicDirResolution(t *testing.T) {
	// negative control: check the env with os.Getenv emptiness instead of presence — an explicitly
	// empty PSTACK_ROUTING_DIR silently falls through instead of being the (unusable) override the
	// operator set (rule 11).
	t.Setenv("PSTACK_ROUTING_DIR", "/somewhere/else")
	if DynamicDir("/data") != "/somewhere/else" {
		t.Fatal("env override must win")
	}
	t.Setenv("PSTACK_ROUTING_DIR", "")
	if DynamicDir("/data") != "" {
		t.Fatal("presence decides, not emptiness")
	}
	os.Unsetenv("PSTACK_ROUTING_DIR")
	// No env, no /etc/traefik/dynamic on a dev machine: the host-side path under the data dir.
	if got := DynamicDir("/data"); got != filepath.Join("/data", "control", "traefik-dynamic") && got != "/etc/traefik/dynamic" {
		t.Fatalf("fallback: %s", got)
	}
}
