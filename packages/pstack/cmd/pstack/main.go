// The pstack binary. Everything lives in internal/; this file only hands argv and the process
// environment to the CLI and exits with its code.
package main

import (
	"fmt"
	"os"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/cli"
	// cloudinit fills the swarm.CloudInit seam at init: the binary must link it for
	// `GET /api/swarm/join?format=cloud-config` and `pstack swarm join` to render a worker.
	_ "github.com/samishal1998/preview-stacks/packages/pstack/internal/cloudinit"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

func main() {
	args, ex := cli.ParseArgs(os.Args[1:], os.LookupEnv)
	if ex != nil {
		exit(ex)
	}
	if args.Version {
		fmt.Println(version.Get())
		os.Exit(cli.ExitOK)
	}
	if args.Help {
		fmt.Print(cli.Usage(version.Get()))
		os.Exit(cli.ExitOK)
	}
	if !cli.IsCommand(args.Cmd) {
		exit(cli.UnknownCommand(args.Cmd, version.Get()))
	}
	switch args.Cmd {
	case "serve":
		exit(cli.Serve(cli.ServeOptions{UIHTML: pstack.UIHTML, ShareHTML: pstack.ShareHTML, Stdout: os.Stdout, Stderr: os.Stderr}))
	case "healthcheck":
		exit(cli.Healthcheck(nil))
	}
	// The rest of the commands land with Phase 6.
	exit(&cli.Exit{Code: cli.ExitUsage, Msg: `command "` + args.Cmd + `" is not in this build yet`})
}

func exit(e *cli.Exit) {
	if e == nil {
		os.Exit(0)
	}
	if e.Msg != "" {
		fmt.Fprintln(os.Stderr, "pstack: "+e.Msg)
	}
	os.Exit(e.Code)
}
