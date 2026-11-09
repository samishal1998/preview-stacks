package scaler

import (
	"embed"
	"io/fs"
)

// `all:` so the dot-file lock files are embedded too.
//
//go:embed all:presets
var presetsFS embed.FS

// Builtins is every built-in preset: name → file name → content. A fresh map each call.
// Sub-directories (a stray .terraform/ from a hand run) are skipped.
func Builtins() map[string]map[string]string {
	out := map[string]map[string]string{}
	dirs, _ := fs.ReadDir(presetsFS, "presets")
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files := map[string]string{}
		entries, _ := fs.ReadDir(presetsFS, "presets/"+d.Name())
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			b, _ := fs.ReadFile(presetsFS, "presets/"+d.Name()+"/"+e.Name())
			files[e.Name()] = string(b)
		}
		out[d.Name()] = files
	}
	return out
}
