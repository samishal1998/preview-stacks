// Package pstack carries the five files the binary embeds. Explicit paths, never a glob: the
// READMEs beside the assets must not ship, and a pattern that matches nothing is a compile error —
// which is exactly the failure mode `with { type: 'text' }` had at bundle time, moved earlier.
package pstack

import _ "embed"

// UIHTML is the basic web UI, served for every non-/api/ path.
//
//go:embed ui/index.html
var UIHTML string

// ShareHTML is the page a share link opens.
//
//go:embed ui/share.html
var ShareHTML string

// ControlTemplate is the control stack's compose file, rendered by init.
//
//go:embed templates/control/docker-compose.yml
var ControlTemplate string

// CloudInitTemplate is the cloud-config user-data template.
//
//go:embed templates/cloud-init.tpl.yaml
var CloudInitTemplate string

// PackageJSON is the workspace manifest — the version of record (lockstep with the TS packages).
//
//go:embed package.json
var PackageJSON []byte
