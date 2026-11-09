package scaler

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// TofuVersion is the pinned OpenTofu release; tofuSums are its zips' sha256 per architecture,
// from the release's SHA256SUMS. A bump changes all three together.
const TofuVersion = "1.13.9"

var tofuSums = map[string]string{
	"amd64": "bc44eb6b06446ba83bd4072114891987f590f66cca712737a7f1147c50c539ce",
	"arm64": "04f7c93828b859d420269ed30f923ffebf09157b5d7fb95bf8d88a4d208b7fa3",
}

var tofuBase = "https://github.com/opentofu/opentofu/releases/download"

// EnsureTofu returns <binDir>/tofu-<TofuVersion>, downloading and checking it first when absent.
// The version is in the name, so a bump downloads the new pin (W-R8). The binary is written to a
// temp file and renamed, so a half download never runs.
func EnsureTofu(binDir string, c *http.Client) (string, error) {
	path := filepath.Join(binDir, "tofu-"+TofuVersion)
	if fi, err := os.Stat(path); err == nil && fi.Mode()&0o111 != 0 {
		return path, nil
	}
	sum, ok := tofuSums[runtime.GOARCH]
	if runtime.GOOS != "linux" || !ok {
		return "", fmt.Errorf("no pinned OpenTofu for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if c == nil {
		c = http.DefaultClient
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("tofu_%s_linux_%s.zip", TofuVersion, runtime.GOARCH)
	resp, err := c.Get(tofuBase + "/v" + TofuVersion + "/" + name)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", name, resp.Status)
	}
	zf, err := os.CreateTemp(binDir, "tofu-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(zf.Name())
	defer zf.Close()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(zf, h), resp.Body)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", name, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return "", fmt.Errorf("%s: sha256 %s, want %s", name, got, sum)
	}
	zr, err := zip.NewReader(zf, size)
	if err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if f.Name == "tofu" {
			return path, extract(f, path)
		}
	}
	return "", fmt.Errorf("%s has no tofu binary", name)
}

func extract(f *zip.File, path string) error {
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), "tofu-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	_, err = io.Copy(tmp, src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return err
}

// Tofu runs one OpenTofu command in Dir (<StateDir>/tofu/<preset>). StateDir is HOME and holds
// the plugin cache (W-R7). Callers add -no-color and -input=false themselves.
type Tofu struct {
	Bin, Dir, StateDir string
	Creds              map[string]string
}

// Run runs tofu with exactly P-R8's environment — never the scaler's own, so never PSTACK_TOKEN.
// The combined output comes back scrubbed; on failure the error is its last 20 lines. No context:
// a started command runs to its end (spec, SIGTERM row).
func (t Tofu) Run(args ...string) (string, error) {
	if t.StateDir == "" {
		return "", errors.New("scaler: Tofu.StateDir is unset")
	}
	plugins := filepath.Join(t.StateDir, "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		return "", err
	}
	cmd := exec.Command(t.Bin, args...)
	cmd.Dir = t.Dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.StateDir,
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"TF_PLUGIN_CACHE_DIR=" + plugins,
	}
	for k, v := range t.Creds {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	b, err := cmd.CombinedOutput()
	out := Scrub(string(b), t.Creds)
	if err != nil {
		if tail := Tail(out, 20); tail != "" {
			return out, errors.New(tail)
		}
		return out, err
	}
	return out, nil
}
