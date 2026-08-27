// `GET /api/control/runtime` and `POST /api/control/restart` — the control stack, visible.
//
// Every other runtime page shows a deployment; this one shows the machinery that serves them:
// traefik, pstack itself, the optional advanced UI. The fields that earn the page are restart
// count and OOM-killed — a Traefik restart wipes its in-memory ACME challenge tokens, so
// certificates stop issuing while everything reports healthy, and until this page the only
// witness was a default certificate's notBefore timestamp.
//
// The restart is scoped by a refusal, not a role: even root may not restart `pstack` through the
// API, because it is the container answering the request — inspect.RestartControlService owns the
// rule and the sentence. Everything else here is maintainer's, like the other host-configuration
// surfaces (permissions.go).
package api

import (
	"errors"
	"net/http"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
)

func (s *Server) controlRuntime(w http.ResponseWriter) error {
	writeJSON(w, 200, inspect.ControlRuntime(s.host))
	return nil
}

func (s *Server) controlRestart(w http.ResponseWriter, r *http.Request) error {
	body := bodyObject(r)
	var service string
	if body != nil {
		if v, _ := body.Get("service"); v != nil {
			if sv, ok := v.(string); ok {
				service = sv
			}
		}
	}
	if service == "" {
		writeError(w, 400, "body must be { service } — a control service name from GET /api/control/runtime")
		return nil
	}
	name, err := inspect.RestartControlService(s.host, service)
	switch {
	case errors.Is(err, inspect.ErrRestartSelf):
		writeError(w, 400, err.Error())
	case errors.Is(err, inspect.ErrNoService):
		writeError(w, 404, `no control service named "`+service+`" — GET /api/control/runtime lists them`)
	case errors.Is(err, inspect.ErrNoDocker):
		writeError(w, 503, "docker did not answer")
	case err != nil:
		writeError(w, 500, err.Error())
	default:
		writeJSON(w, 200, jsonx.O("ok", true, "service", service, "container", name))
	}
	return nil
}
