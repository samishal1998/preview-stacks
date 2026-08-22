// Package version answers "which pstack is this" from two sources, in order: the -X flag a release
// build stamps, else the `version` field of the embedded package.json. The fallback is what makes
// `go run`, `go test` and a plain `go build` report the lockstep version — three tests pin it, and
// `build-image` stamps it into a Dockerfile header.
package version

import (
	"encoding/json"
	"strings"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
)

// Version is set by `-ldflags "-X .../internal/version.Version=0.29.0"`. Empty means "read package.json".
var Version string

// Get returns the version string, never empty.
func Get() string {
	if v := strings.TrimSpace(Version); v != "" {
		return strings.TrimPrefix(v, "v")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(pstack.PackageJSON, &pkg); err != nil || pkg.Version == "" {
		return "0.0.0-unknown"
	}
	return pkg.Version
}
