// The pstack binary. Everything lives in internal/; this file only hands argv and the process
// environment to the CLI and exits with its code.
package main

import (
	"os"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/cli"
	// cloudinit fills the swarm.CloudInit seam at init: the binary must link it for
	// `GET /api/swarm/join?format=cloud-config` and `pstack swarm join` to render a worker.
	_ "github.com/samishal1998/preview-stacks/packages/pstack/internal/cloudinit"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, UIHTML: pstack.UIHTML, ShareHTML: pstack.ShareHTML, OpenAPISpec: pstack.OpenAPISpec}))
}
