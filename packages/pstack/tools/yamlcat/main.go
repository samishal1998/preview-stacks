// yamlcat parses YAML files with pstack's own parser and prints each one as JSON, one line per
// file: {"path":…,"json":…} or {"path":…,"error":…}.
//
// It exists so a YAML file can be read by BOTH parsers — Bun's (what pstack used through 0.28.0)
// and this one — and the two results compared. See packages/conformance/diff/corpus.ts. Not shipped: nothing in
// cmd/pstack imports it.
package main

import (
	"fmt"
	"os"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

func main() {
	for _, path := range os.Args[1:] {
		b, err := os.ReadFile(path)
		if err != nil {
			emit(path, nil, err)
			continue
		}
		v, err := yamlx.Parse(b)
		if err != nil {
			emit(path, nil, err)
			continue
		}
		j, err := jsonx.Marshal(v)
		if err != nil {
			emit(path, nil, err)
			continue
		}
		emit(path, j, nil)
	}
}

func emit(path string, j []byte, err error) {
	if err != nil {
		fmt.Println(string(jsonx.Must(jsonx.O("path", path, "error", err.Error()))))
		return
	}
	fmt.Println(string(jsonx.Must(jsonx.O("path", path, "json", string(j)))))
}
