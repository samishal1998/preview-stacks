// Package pstack carries the eight files the binary embeds. Explicit paths, never a glob: the
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

// LokiConfig is Loki's config file, which init writes beside the control compose file with
// `--logging loki`. Embedded as-is, never rendered: nothing in it varies by host, and it holds no
// credential.
//
//go:embed templates/control/loki/config.yaml
var LokiConfig string

// GrafanaDatasources is Grafana's provisioned Loki datasource, which init writes to
// control/grafana/datasources/loki.yaml with `--logging loki`. Embedded as-is for LokiConfig's
// reasons: nothing in it varies by host, and it holds no credential.
//
//go:embed templates/control/grafana/datasources.yaml
var GrafanaDatasources string

// CloudInitTemplate is the cloud-config user-data template.
//
//go:embed templates/cloud-init.tpl.yaml
var CloudInitTemplate string

// PackageJSON is the workspace manifest — the version of record (lockstep with the TS packages).
//
//go:embed package.json
var PackageJSON []byte

// OpenAPISpec is the API's own OpenAPI document, served at `/api/openapi.yaml` and, converted, at
// `/api/openapi.json`.
//
// The SAME file oascmd-gen reads to generate `pstack api`, so the served document and the shipped
// commands cannot describe different APIs — and a route missing from it fails a test before either
// exists. Embedding costs ~34KB in a 17MB binary; fetching it from anywhere at runtime would mean a
// host could serve a spec that does not match the binary answering the requests.
//
//go:embed api/openapi.yaml
var OpenAPISpec []byte
