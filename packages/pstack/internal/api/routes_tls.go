// `/api/tls` — the host's certificate mode, and the bring-your-own wildcard (`dns-persist-01`).
//
// There is deliberately NO stored "desired mode" setting: the mode is DERIVED — a stored wildcard
// (routing.WildcardYAML present) is `dns-persist-01`, otherwise whatever Traefik's own argv says.
// A setting could disagree with the artifacts; the artifacts cannot disagree with themselves.
//
// The wildcard PUT takes the pair in plaintext and the key never comes back out (invariant 15) —
// the GET returns the certificate's public facts only. The write is ADMIN's for BLAST RADIUS, not
// as a security boundary: it changes what every hostname on the host presents, and the redeploy
// that follows touches every stack. A maintainer can already reach equivalent power through
// /api/routing (see permissions.go). Reading the status and running the redeploy loop are
// maintainer's, like the other host surfaces.
//
// `POST /api/tls/redeploy` is tls-challenge.md's migration loop, server-side: per-PR router labels
// are stamped at deploy time from the mode the host was in THEN, so entering or leaving a wildcard
// mode requires each stack to deploy once more. Asleep stacks are skipped — their labels
// regenerate on wake, and redeploying them here would BE a wake nobody asked for.
package api

import (
	"net/http"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jobs"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
)

// challenge is the ONE answer the API gives about the host's mode: the stored wildcard overrides
// Traefik's argv, because in dns-persist mode the argv keeps whatever `init` configured.
func (s *Server) challenge() inspect.Challenge {
	if s.routing.WildcardActive() {
		return inspect.DNSPersist
	}
	return inspect.DetectChallenge(s.host)
}

func (s *Server) tlsStatus(w http.ResponseWriter) error {
	traefik := inspect.DetectChallenge(s.host)
	mode := traefik
	note := "Traefik resolves certificates itself in this mode. PUT /api/tls/wildcard switches to dns-persist-01 without touching the control stack."
	if s.routing.WildcardActive() {
		mode = inspect.DNSPersist
		note = "A stored wildcard covers every preview by SNI. New deploys stop ordering per-hostname certificates; DELETE /api/tls/wildcard returns the host to Traefik-native resolution (then redeploy)."
	}
	writeJSON(w, 200, jsonx.O(
		"mode", string(mode),
		// What Traefik's own flags still say — under dns-persist-01 the resolver stays configured
		// (harmless: no per-PR router references it after a redeploy) and this names it honestly.
		"traefik", string(traefik),
		"wildcard", s.routing.WildcardInfo(),
		"note", note,
	))
	return nil
}

func (s *Server) tlsWildcardPut(w http.ResponseWriter, r *http.Request) error {
	body := bodyOrEmpty(r)
	cert, key := "", ""
	if v, _ := body.Get("cert"); v != nil {
		cert, _ = v.(string)
	}
	if v, _ := body.Get("key"); v != nil {
		key, _ = v.(string)
	}
	if cert == "" || key == "" {
		writeError(w, 400, "body must be { cert, key } — both PEM, the certificate with its chain and the private key")
		return nil
	}
	info, err := s.routing.SetWildcard(cert, key, s.opts.Domain)
	if err != nil {
		if routing.IsError(err) {
			writeError(w, 400, err.Error())
			return nil
		}
		return err
	}
	writeJSON(w, 200, jsonx.O(
		"ok", true,
		"wildcard", info,
		"note", "Stored and pointed at Traefik — new deploys inherit it immediately. Stacks deployed before this still carry tls.certresolver labels and keep ordering their own certificates: POST /api/tls/redeploy regenerates them.",
	))
	return nil
}

func (s *Server) tlsWildcardDelete(w http.ResponseWriter) error {
	removed, err := s.routing.RemoveWildcard()
	if err != nil {
		if routing.IsError(err) {
			writeError(w, 400, err.Error())
			return nil
		}
		return err
	}
	if !removed {
		writeError(w, 404, "no wildcard is stored — the host is already on Traefik-native resolution")
		return nil
	}
	writeJSON(w, 200, jsonx.O(
		"ok", true,
		"note", "Removed. The host is back on Traefik-native resolution ("+string(inspect.DetectChallenge(s.host))+"). Stacks deployed under the wildcard carry tls=true alone and now have NO certificate: POST /api/tls/redeploy gives them their resolver back.",
	))
	return nil
}

func (s *Server) tlsRedeploy(w http.ResponseWriter, who *auth.Principal) error {
	metas, err := s.registry.List()
	if err != nil {
		return err
	}
	started := []jsonx.Object{}
	skipped := []jsonx.Object{}
	for _, m := range metas {
		if m.Sleep != nil {
			// A redeploy IS a wake. The labels of a sleeping stack regenerate on its own wake anyway.
			skipped = append(skipped, jsonx.O("id", m.ID, "reason", "asleep — labels regenerate on wake"))
			continue
		}
		dep, err := s.registry.Get(m.ID)
		if err != nil || dep == nil {
			skipped = append(skipped, jsonx.O("id", m.ID, "reason", "vanished between list and read"))
			continue
		}
		st, err := s.resolveDep(m.ID, nil)
		if err != nil {
			skipped = append(skipped, jsonx.O("id", m.ID, "reason", "does not resolve: "+err.Error()))
			continue
		}
		o := lifecycleOptions{By: terminal.ActorOf(*who), Reason: "tls: redeploy to regenerate router labels"}
		job, ok := s.startLifecycle(m.ID, dep, st, jobs.Up, o)
		if !ok {
			skipped = append(skipped, jsonx.O("id", m.ID, "reason", "held — another operation owns the stack right now"))
			continue
		}
		started = append(started, jsonx.O("id", m.ID, "job", job.ID))
	}
	writeJSON(w, 200, jsonx.O(
		"started", started,
		"skipped", skipped,
		"note", "Each redeploy queues like any other job (a busy stack keeps one waiting job, newest wins). Watch them on /api/jobs.",
	))
	return nil
}
