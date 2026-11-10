package scaler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/specs"
)

// Preset is what the scaler and the API read off a preset's files.
type Preset struct {
	PerWorker []string `json:"perWorker"`
	Shared    []string `json:"shared"`
	Distro    string   `json:"distro"`
	Hash      string   `json:"hash"`
	LockFile  bool     `json:"lockFile"`
}

const lockFileName = ".terraform.lock.hcl"

var (
	fileNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+\.tf$`)
	variableRe = regexp.MustCompile(`(?m)^\s*variable\s+"([A-Za-z0-9_]+)"`)
	attrRe     = regexp.MustCompile(`^\s*([A-Za-z0-9_]+)\s*=`)
	backendRe  = regexp.MustCompile(`\bbackend\s+"|\bcloud\s*\{`) // unanchored (W-R9)
	distroRe   = regexp.MustCompile(`(?m)^#\s*pstack-distro:\s*(\S+)`)
	objectRe   = regexp.MustCompile(`object\s*\(\s*\{`)
)

// Variables the scaler fills itself when a preset declares them; never shared.
var filled = map[string]bool{"workers": true, "manager_address": true, "host_id": true}

// ParsePreset checks a preset against the contract and reads its variables.
//
// ponytail: regexes and brace counting, not an HCL parser — a `{` inside a string in the workers
// block miscounts. `tofu validate` is the real check; this only has to find the variable names.
func ParsePreset(files map[string]string) (Preset, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		if name != lockFileName && !fileNameRe.MatchString(name) {
			return Preset{}, &specs.Error{Msg: fmt.Sprintf("invalid file name %q: want <name>.tf or %s", name, lockFileName)}
		}
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	var tf strings.Builder
	for _, name := range names {
		h.Write([]byte(name + "\x00" + files[name] + "\x00"))
		if name != lockFileName {
			tf.WriteString(files[name])
			tf.WriteString("\n")
		}
	}
	src := tf.String()

	if backendRe.MatchString(src) {
		return Preset{}, &specs.Error{Msg: "backend or cloud block: state is local"}
	}

	p := Preset{PerWorker: []string{}, Shared: []string{}, Hash: hex.EncodeToString(h.Sum(nil))}
	_, p.LockFile = files[lockFileName]
	if m := distroRe.FindStringSubmatch(src); m != nil {
		p.Distro = m[1]
	}

	workersAt := -1
	for _, m := range variableRe.FindAllStringSubmatchIndex(src, -1) {
		name := src[m[2]:m[3]]
		if name == "workers" {
			workersAt = m[1]
		}
		if !filled[name] && !slices.Contains(p.Shared, name) {
			p.Shared = append(p.Shared, name)
		}
	}
	if workersAt < 0 {
		return Preset{}, &specs.Error{Msg: `no variable "workers"`}
	}

	block, _ := braced(src, workersAt)
	userData := false
	if loc := objectRe.FindStringIndex(block); loc != nil {
		body, _ := braced(block, loc[0])
		for _, piece := range splitTop(body) {
			m := attrRe.FindStringSubmatch(piece)
			switch {
			case m == nil:
			case m[1] == "user_data":
				userData = true
			case !slices.Contains(p.PerWorker, m[1]):
				p.PerWorker = append(p.PerWorker, m[1])
			}
		}
	}
	if !userData {
		return Preset{}, &specs.Error{Msg: `variable "workers" has no user_data: want map(object({ user_data = string, … }))`}
	}
	sort.Strings(p.PerWorker)
	sort.Strings(p.Shared)
	return p, nil
}

// braced is the text inside the first {…} at or after from, braces balanced.
func braced(s string, from int) (string, bool) {
	start := strings.IndexByte(s[from:], '{')
	if start < 0 {
		return "", false
	}
	start += from
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start+1 : i], true
			}
		}
	}
	return s[start+1:], false
}

// splitTop splits on newlines and commas outside any (), [] or {} (W-R10), so both
// one-attribute-per-line and `{ user_data = string, size = string }` read the same.
func splitTop(s string) []string {
	var out []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '\n', ',':
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	return append(out, s[last:])
}

// ValidPresetName applies the spec-name rule and refuses a built-in name.
func ValidPresetName(name string) error {
	if err := specs.AssertValidSpecName(name); err != nil {
		return &specs.Error{Msg: strings.Replace(err.Error(), "spec name", "preset name", 1)}
	}
	if _, ok := Builtins()[name]; ok {
		return &specs.Error{Msg: fmt.Sprintf("preset %q is built in", name)}
	}
	return nil
}
