// `GET /api/openapi.yaml` and `GET /api/openapi.json` — the API's own description, as served by the
// binary answering the requests.
//
// ── WHY IT IS THE EMBEDDED FILE AND NOT A GENERATED ONE ──────────────────────────────────────────
//
// The same document oascmd-gen reads to generate `pstack api`, embedded verbatim. So the spec a
// client fetches, the commands the CLI ships, and the routes `routes.go` serves are one artifact
// checked three ways: `openapi_coverage_test.go` fails when a route has no path or a path has no
// route, and CI's `generated` job fails when the document moves without the generated code.
//
// A spec assembled at runtime from the route table would be a fourth thing to keep true, and the
// first one to drift, because nothing would compare it to anything.
//
// ── TWO FORMATS, ONE SOURCE ──────────────────────────────────────────────────────────────────────
//
// YAML is the file, byte for byte — no re-serialisation, so comments and key order survive and what
// you fetch is what the repo holds. JSON is that file parsed by `yamlx` (the same YAML-1.2 reader
// the specs go through) and re-emitted by `jsonx`, which preserves insertion order via `omap` — so
// `paths` reads in the order it was written rather than sorted, and a diff between two versions is
// legible.
//
// Converted ONCE, on first request, not per request and not at init: `pstack up` on a laptop must
// not pay for a document it will never serve, and a control plane serving it a thousand times must
// not pay a thousand times.
//
// ── UNAUTHENTICATED, LIKE /api/health ────────────────────────────────────────────────────────────
//
// A description of an API is not a secret — every route in it enforces its own auth, the repository
// is public, and a client generating against it has no token yet by definition. It sits in the
// pre-gate for the same reason health does.
package api

import (
	"net/http"
	"strings"
	"sync"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// openAPIJSON is the YAML document converted to JSON, computed once.
//
// `sync.OnceValues` rather than a package-level var: the spec comes from Options, so it is not known
// until a server exists, and a failed conversion must be answerable rather than fatal — a broken
// embedded spec should cost this one route, not the process.
type openAPIDoc struct {
	yaml []byte
	json func() ([]byte, error)
}

func newOpenAPIDoc(spec []byte) *openAPIDoc {
	return &openAPIDoc{
		yaml: spec,
		json: sync.OnceValues(func() ([]byte, error) {
			v, err := yamlx.Parse(spec)
			if err != nil {
				return nil, err
			}
			b, err := jsonx.MarshalIndent(v)
			if err != nil {
				return nil, err
			}
			return append(b, '\n'), nil
		}),
	}
}

// openAPI serves the document. Returns false when the path is not one of the two, so the caller
// falls through.
func (s *Server) openAPI(w http.ResponseWriter, r *http.Request, path string) bool {
	var wantJSON bool
	switch path {
	case "/api/openapi.yaml":
		wantJSON = false
	case "/api/openapi.json":
		wantJSON = true
	default:
		return false
	}
	if s.spec == nil || len(s.spec.yaml) == 0 {
		// A binary built without the asset. Not a 500: nothing is broken for any other route, and
		// the sentence says which of the two possible causes it is.
		writeError(w, 404, "this build carries no OpenAPI document")
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, 405, "use GET")
		return true
	}
	body, ctype := s.spec.yaml, "application/yaml; charset=utf-8"
	if wantJSON {
		b, err := s.spec.json()
		if err != nil {
			writeError(w, 500, "the embedded OpenAPI document is not parseable: "+err.Error())
			return true
		}
		body, ctype = b, "application/json; charset=utf-8"
	}
	h := w.Header()
	h.Set("content-type", ctype)
	// The document changes only when the binary does, so it is cacheable for as long as the caller
	// keeps talking to this version — but never longer, which is what the version in the ETag says.
	h.Set("etag", `"`+strings.ReplaceAll(s.opts.Version, `"`, "")+`"`)
	h.Set("cache-control", "public, max-age=300")
	w.WriteHeader(200)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return true
}
