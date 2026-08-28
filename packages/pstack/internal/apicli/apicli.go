// Package apicli is the `pstack api …` command tree, GENERATED from
// `packages/pstack/api/openapi.yaml`.
//
// Everything except this file is generated: `zz_generated.go` carries one constructor per operation
// and `NewCommandTree`, and `oascmd.lock.json` records the CLI surface so a later regeneration can
// say exactly which commands a spec change would move or remove. Regenerate with:
//
//	go generate ./packages/pstack/internal/apicli
//
// and run it WITHOUT `--on-drift`: the default refuses a change that breaks an existing command,
// which is the whole reason the lock file is checked in. A rename that is genuinely wanted is
// accepted deliberately, once, with `--on-drift=all` — never as part of an unrelated edit.
//
// The generator is a TOOL dependency, not a shipped one: it pulls libopenapi, and the code it emits
// imports only cobra, pflag and oascmd's executor. That is what keeps `CGO_ENABLED=0` and a small
// binary intact while the spec still drives the surface.
package apicli

//go:generate go run github.com/samishal1998/openapi-commands/cmd/oascmd-gen -spec ../../api/openapi.yaml -package apicli -out zz_generated.go

// OperationCount is how many commands the tree holds, for the one line `pstack --help` prints about
// `api`.
//
// A CONSTANT, checked by a test against the lock file, rather than a walk of the tree at startup:
// building the tree needs an `ExecOptions` — which needs a resolved PSTACK_API_URL and a token — and
// `pstack --help` must work on a laptop with neither. The test is what keeps it honest.
const OperationCount = 79
