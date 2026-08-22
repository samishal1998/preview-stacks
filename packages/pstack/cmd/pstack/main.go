// The pstack binary. Everything lives in internal/; this file only hands argv and the process
// environment to the CLI and exits with its code.
package main

import (
	"fmt"
	"os"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

func main() {
	// Placeholder until internal/cli lands: the version is the one contract that already exists.
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-V") {
		fmt.Println(version.Get())
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "pstack: the Go port is under construction — see packages/conformance")
	os.Exit(3)
}
