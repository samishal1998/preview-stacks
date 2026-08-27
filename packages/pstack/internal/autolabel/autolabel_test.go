package autolabel

// Port of test/stack.test.ts 'generated Traefik labels — six pieces of boilerplate from one',
// including 'refusals' and 'materializing the file'. Compose documents are built from YAML (the
// JS test built object literals); specs come from testdata.

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

func specFrom(t *testing.T, file string, env map[string]string) *spec.Stack {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	st, err := spec.Parse(string(b), env, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func s(t *testing.T) *spec.Stack { return specFrom(t, "spec.yml", map[string]string{"PR": "7"}) }

func doc(t *testing.T, yaml string) *omap.Map {
	t.Helper()
	v, err := yamlx.ParseString(yaml)
	if err != nil {
		t.Fatal(err)
	}
	return v.(*omap.Map)
}

func labelsOf(t *testing.T, d *omap.Map, svc string) []string {
	t.Helper()
	raw := d.GetMap("services").GetMap(svc).GetSlice("labels")
	out := make([]string, len(raw))
	for i, l := range raw {
		out[i] = l.(string)
	}
	return out
}

// generatedFor is r.Generated[svc] — a []string, so omap's GetSlice (which wants []any) does not see it.
func generatedFor(t *testing.T, r *MaterializeResult, svc string) []string {
	t.Helper()
	v, ok := r.Generated.Get(svc)
	if !ok {
		t.Fatalf("no generated entry for %s: %s", svc, jsonOf(t, r.Generated))
	}
	return v.([]string)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := jsonx.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func augment(t *testing.T, d *omap.Map, st *spec.Stack, ch Challenge) *AugmentResult {
	t.Helper()
	r, err := AugmentComposeDoc(AugmentArgs{Doc: d, Spec: st, Challenge: ch})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGeneratedTraefikLabels(t *testing.T) {
	t.Run("labels are read from both compose forms", func(t *testing.T) {
		// negative control: split at the LAST `=` in LabelsToMap — the rule value loses its tail.
		if got := jsonOf(t, LabelsToMap([]any{"a=1", "b=2", "flag"})); got != `{"a":"1","b":"2","flag":""}` {
			t.Errorf("list form: %s", got)
		}
		if got := jsonOf(t, LabelsToMap(omap.From("a", int64(1), "b", "two"))); got != `{"a":"1","b":"two"}` {
			t.Errorf("map form: %s", got)
		}
		if got := jsonOf(t, LabelsToMap(nil)); got != `{}` {
			t.Errorf("absent: %s", got)
		}
		// A value containing '=' keeps all of it — a Traefik rule is full of them.
		b, _ := os.ReadFile("testdata/labels-with-rule.yml")
		list, _ := yamlx.Parse(b)
		m := LabelsToMap(list)
		if m.GetString("r") != "Host(`a.b`) && Path(`/x=1`)" || m.Len() != 1 {
			t.Errorf("rule value: %s", jsonOf(t, m))
		}
	})

	t.Run("one label produces every piece a reachable service needs", func(t *testing.T) {
		// negative control: drop "traefik.enable=true" from `added` — the first assertion fails.
		r := augment(t, doc(t, "services:\n  app:\n    image: nginx:alpine\n    labels: [pstack.routing.port=80]\n"), s(t), HTTP01)
		labels := labelsOf(t, r.Doc, "app")
		for _, want := range []string{
			"traefik.enable=true",
			"traefik.docker.network=preview-ingress",
			// The stack is in the router id: Traefik's namespace is global across the daemon.
			"traefik.http.routers.app-pr-7.rule=Host(`app-pr-7.preview.example.com`)",
			"traefik.http.services.app-pr-7.loadbalancer.server.port=80",
			"traefik.http.routers.app-pr-7.service=app-pr-7",
			// HTTP-01: every hostname resolves its own certificate, so the router needs a resolver.
			"traefik.http.routers.app-pr-7.tls.certresolver=le",
			// The user's own label survives, so the file stays self-describing.
			"pstack.routing.port=80",
		} {
			if !contains(labels, want) {
				t.Errorf("missing %q in %v", want, labels)
			}
		}
		// Networks, on the service and at the root — `external: true` is the whole point.
		if got := jsonOf(t, r.Doc.GetMap("services").GetMap("app").GetSlice("networks")); got != `["default","preview-ingress"]` {
			t.Errorf("service networks: %s", got)
		}
		if got := jsonOf(t, r.Doc.GetMap("networks")); got != `{"default":{},"preview-ingress":{"external":true},"preview-shared":{"external":true}}` {
			t.Errorf("root networks: %s", got)
		}
	})

	t.Run("DNS-01 inverts the certresolver rule", func(t *testing.T) {
		// negative control: change `challenge != DNS01` to `true` — a certresolver label appears.
		// One always-on router holds the wildcard there; a per-router resolver makes each PR order its
		// own certificate and burn the ~50-per-week limit.
		r := augment(t, doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n"), s(t), DNS01)
		labels := labelsOf(t, r.Doc, "app")
		if !contains(labels, "traefik.http.routers.app-pr-7.tls=true") {
			t.Errorf("tls=true missing: %v", labels)
		}
		for _, l := range labels {
			if strings.Contains(l, "certresolver") {
				t.Errorf("certresolver under dns01: %v", labels)
			}
		}
	})

	t.Run("a service with its own traefik labels is left completely alone", func(t *testing.T) {
		// negative control: skip the hasTraefik check — labels get appended and networks invented.
		// The escape hatch, and it must be total: no labels appended, no networks touched.
		b, _ := os.ReadFile("testdata/own-labels.yml")
		original := doc(t, string(b))
		r := augment(t, original, s(t), HTTP01)
		if got, want := jsonOf(t, r.Doc.GetMap("services").GetMap("app")), jsonOf(t, original.GetMap("services").GetMap("app")); got != want {
			t.Errorf("service changed:\n%s\n%s", got, want)
		}
		if r.Generated.Len() != 0 {
			t.Errorf("generated: %s", jsonOf(t, r.Generated))
		}
		if !strings.Contains(r.Skipped.GetString("app"), "its own traefik.* labels") {
			t.Errorf("skipped: %s", jsonOf(t, r.Skipped))
		}
		// And with nothing routed, the root networks are not invented either.
		if r.Doc.Has("networks") {
			t.Errorf("networks invented: %s", jsonOf(t, r.Doc))
		}
	})

	t.Run("a service that asks for nothing is left alone — a database needs no hostname", func(t *testing.T) {
		// negative control: treat a nil request as port 80 — labels appear on db.
		r := augment(t, doc(t, "services:\n  db:\n    image: postgres:17\n"), s(t), HTTP01)
		if r.Doc.GetMap("services").GetMap("db").Has("labels") {
			t.Errorf("db got labels: %s", jsonOf(t, r.Doc))
		}
		if !strings.Contains(r.Skipped.GetString("db"), "not routed") {
			t.Errorf("skipped: %s", jsonOf(t, r.Skipped))
		}
	})

	t.Run("service_name overrides the hostname, and an explicit host overrides both", func(t *testing.T) {
		// negative control: ignore pstack.routing.host in ReadRoutingRequest — api's rule is derived.
		r := augment(t, doc(t, "services:\n  web:\n    image: x\n    labels: [pstack.routing.port=3000, pstack.routing.service_name=frontend]\n  api:\n    image: x\n    labels: [pstack.routing.port=8080, pstack.routing.host=custom.example.com]\n"), s(t), HTTP01)
		if web := labelsOf(t, r.Doc, "web"); !contains(web, "traefik.http.routers.frontend-pr-7.rule=Host(`frontend-pr-7.preview.example.com`)") {
			t.Errorf("web: %v", web)
		}
		if api := labelsOf(t, r.Doc, "api"); !contains(api, "traefik.http.routers.api-pr-7.rule=Host(`custom.example.com`)") {
			t.Errorf("api: %v", api)
		}
	})

	t.Run("existing service networks are appended to, never replaced", func(t *testing.T) {
		// negative control: set `existing = []string{"default"}` unconditionally — preview-shared is lost.
		r := augment(t, doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n    networks: [default, preview-shared]\nnetworks:\n  default: {}\n  preview-shared: {external: true}\n"), s(t), HTTP01)
		if got := jsonOf(t, r.Doc.GetMap("services").GetMap("app").GetSlice("networks")); got != `["default","preview-shared","preview-ingress"]` {
			t.Errorf("networks: %s", got)
		}
	})

	t.Run("a user's own root network definition is never overwritten", func(t *testing.T) {
		// negative control: Set("default", omap.New()) without the Has guard — `mine` is replaced by {}.
		r := augment(t, doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\nnetworks:\n  default: {driver: bridge, name: i-meant-this}\n"), s(t), HTTP01)
		if got := jsonOf(t, r.Doc.GetMap("networks").GetMap("default")); got != `{"driver":"bridge","name":"i-meant-this"}` {
			t.Errorf("default network: %s", got)
		}
	})

	t.Run("a wildcard subdomain router is generated when the spec asked for one", func(t *testing.T) {
		// negative control: compare sub.Profile to req.Host instead of req.Name — no -wild labels.
		withSubs := specFrom(t, "spec-subs.yml", map[string]string{"PR": "7"})
		r := augment(t, doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n"), withSubs, HTTP01)
		labels := labelsOf(t, r.Doc, "app")
		found := false
		for _, l := range labels {
			if strings.HasPrefix(l, "traefik.http.routers.app-pr-7-wild.rule=HostRegexp(") {
				found = true
			}
		}
		if !found {
			t.Errorf("no wild rule: %v", labels)
		}
		// Lower priority than the exact host, which scores on rule length.
		if !contains(labels, "traefik.http.routers.app-pr-7-wild.priority=2") {
			t.Errorf("priority: %v", labels)
		}
		// Same backend as the exact router.
		if !contains(labels, "traefik.http.routers.app-pr-7-wild.service=app-pr-7") {
			t.Errorf("service: %v", labels)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		t.Run("a port that is not a port is refused, not skipped", func(t *testing.T) {
			// negative control: drop the IsInteger/range check — "web" parses as NaN and sails through.
			// Skipping silently is how "I added the label and nothing happened" happens.
			for _, p := range []string{"web", "0", "70000"} {
				_, err := ReadRoutingRequest("app", omap.From("pstack.routing.port", p))
				if err == nil || !strings.Contains(err.Error(), "not a port") {
					t.Errorf("port %q: %v", p, err)
				}
			}
		})

		t.Run("pstack.routing.* without a port is refused", func(t *testing.T) {
			// negative control: return nil, nil when the port is absent — no error.
			_, err := ReadRoutingRequest("app", omap.From("pstack.routing.service_name", "web"))
			if err == nil || !strings.Contains(err.Error(), "no pstack.routing.port") {
				t.Errorf("got %v", err)
			}
		})

		t.Run("no domain at all and no explicit host is refused, naming PREVIEW_DOMAIN", func(t *testing.T) {
			// negative control: default `domain` to "example.com" when empty — no error at all.
			noDomain := specFrom(t, "spec-nodomain.yml", map[string]string{})
			_, err := AugmentComposeDoc(AugmentArgs{Doc: doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n"), Spec: noDomain, Challenge: HTTP01})
			if err == nil || !strings.Contains(err.Error(), "no domain to build a hostname from") {
				t.Fatalf("got %v", err)
			}
			// It names the variable to declare, and says the legacy one still works.
			if !strings.Contains(err.Error(), "PREVIEW_DOMAIN") {
				t.Errorf("got %v", err)
			}
			if !spec.IsSpecError(err) {
				t.Errorf("not a spec error: %T", err)
			}
		})

		t.Run("the legacy DOMAIN alias still produces a hostname", func(t *testing.T) {
			// negative control: resolve the domain from PREVIEW_DOMAIN only — the legacy spec is refused.
			// 0.3.0–0.7.0 read only DOMAIN, so a spec written against those must keep working.
			legacy := specFrom(t, "spec-legacy.yml", map[string]string{})
			r := augment(t, doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n"), legacy, HTTP01)
			if labels := labelsOf(t, r.Doc, "app"); !contains(labels, "traefik.http.routers.app-s.rule=Host(`app-s.legacy.example.com`)") {
				t.Errorf("labels: %v", labels)
			}
		})
	})

	t.Run("materializing the file", func(t *testing.T) {
		quiet := exec.NewFake(nil, "")
		http01 := HTTP01
		write := func(t *testing.T, dir, body string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(body), 0o666); err != nil {
				t.Fatal(err)
			}
		}
		exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

		t.Run("a compose file with no pstack.routing.* is used unchanged, with nothing written", func(t *testing.T) {
			// negative control: drop the `!isSwarm && generated.Len() == 0` return — the second case
			// (the text mentions pstack.routing. but nothing was generated) writes the file.
			dir := t.TempDir()
			write(t, dir, "services:\n  app:\n    image: nginx\n")
			r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
			if err != nil || r.File != "docker-compose.yml" {
				t.Errorf("got %+v, %v", r, err)
			}
			if exists(filepath.Join(dir, GeneratedCompose)) {
				t.Error("generated file written")
			}
			// The text mentions the label but the only service carrying it opted out with its own
			// traefik.* labels: nothing generated, so the submitted file is what compose reads.
			write(t, dir, "services:\n  app:\n    image: nginx\n    labels: [pstack.routing.port=80, traefik.enable=true]\n")
			r, err = MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
			if err != nil || r.File != "docker-compose.yml" || r.Skipped.GetString("app") == "" {
				t.Errorf("got %+v, %v", r, err)
			}
			if exists(filepath.Join(dir, GeneratedCompose)) {
				t.Error("generated file written with nothing generated")
			}
		})

		t.Run("the derived file is written beside the original, which is untouched", func(t *testing.T) {
			// negative control: write to `source` instead of the generated path — the original changes.
			dir := t.TempDir()
			submitted := "services:\n  app:\n    image: nginx:alpine\n    labels:\n      - pstack.routing.port=80\n"
			write(t, dir, submitted)
			r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
			if err != nil || r.File != GeneratedCompose {
				t.Fatalf("got %+v, %v", r, err)
			}
			// Your file is never modified — the edit form must still show what you submitted.
			if b, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml")); string(b) != submitted {
				t.Errorf("original modified: %q", b)
			}
			// JSON, which every YAML parser reads identically — including Go's, which compose uses.
			generated, _ := os.ReadFile(filepath.Join(dir, GeneratedCompose))
			asYAML, err := yamlx.Parse(generated)
			if err != nil {
				t.Fatal(err)
			}
			asJSON, err := omap.Parse(generated)
			if err != nil {
				t.Fatal(err)
			}
			if jsonOf(t, asYAML) != jsonOf(t, asJSON) {
				t.Errorf("YAML and JSON readings differ:\n%s\n%s", jsonOf(t, asYAML), jsonOf(t, asJSON))
			}
			parsed := asYAML.(*omap.Map)
			if !contains(labelsOf(t, parsed, "app"), "traefik.enable=true") {
				t.Errorf("labels: %s", generated)
			}
			if got := jsonOf(t, parsed.GetMap("networks").GetMap("preview-ingress")); got != `{"external":true}` {
				t.Errorf("ingress: %s", got)
			}
			// JSON.stringify(out, null, 2) + "\n": two-space indent WITH a trailing newline.
			if !strings.HasSuffix(string(generated), "}\n") || !strings.HasPrefix(string(generated), "{\n  \"services\": {\n") {
				t.Errorf("shape: %q", generated)
			}
		})

		t.Run("regenerating produces an identical file — up and down cannot disagree", func(t *testing.T) {
			// negative control: append a timestamp comment to the written file — the two reads differ.
			dir := t.TempDir()
			write(t, dir, "services:\n  app:\n    image: x\n    labels:\n      - pstack.routing.port=80\n")
			first, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
			if err != nil {
				t.Fatal(err)
			}
			a, _ := os.ReadFile(filepath.Join(dir, first.File))
			if _, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01}); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(dir, first.File))
			if string(a) != string(b) || len(a) == 0 {
				t.Errorf("differs:\n%s\n%s", a, b)
			}
		})

		t.Run("an unreadable compose file falls back to the original rather than guessing", func(t *testing.T) {
			// negative control: return the read error — the call fails instead of falling back.
			r, err := MaterializeCompose(MaterializeArgs{Dir: filepath.Join(t.TempDir(), "absent"), Spec: s(t), Runner: quiet, Challenge: &http01})
			if err != nil || r.File != "docker-compose.yml" {
				t.Errorf("got %+v, %v", r, err)
			}
			if r.Generated == nil || r.Skipped == nil || r.Notes == nil {
				t.Errorf("nil collections: %+v", r)
			}
		})

		t.Run("invalid YAML and a non-mapping are spec errors naming the file", func(t *testing.T) {
			// negative control: return untouched() on a parse error — no error surfaces.
			dir := t.TempDir()
			write(t, dir, "services: [\n  pstack.routing.port")
			_, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
			if err == nil || !strings.HasPrefix(err.Error(), "compose file docker-compose.yml is not valid YAML: ") || !spec.IsSpecError(err) {
				t.Errorf("got %v", err)
			}
			write(t, dir, "- pstack.routing.port=80\n")
			_, err = MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet, Challenge: &http01})
			if err == nil || err.Error() != "compose file docker-compose.yml must be a mapping" {
				t.Errorf("got %v", err)
			}
		})

		t.Run("without a challenge the probe seam is consulted", func(t *testing.T) {
			// negative control: ignore DetectChallenge and use HTTP01 — the certresolver label appears.
			prev := DetectChallenge
			DetectChallenge = func(exec.Runner) Challenge { return DNS01 }
			defer func() { DetectChallenge = prev }()
			dir := t.TempDir()
			write(t, dir, "services:\n  app:\n    image: x\n    labels:\n      - pstack.routing.port=80\n")
			r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: quiet})
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range generatedFor(t, r, "app") {
				if strings.Contains(l, "certresolver") {
					t.Errorf("dns01 from the seam was ignored: %v", r.Generated)
				}
			}
		})

		t.Run("the seam's DEFAULT probes the running traefik, not a constant", func(t *testing.T) {
			// negative control: restore `return Unknown` as the var's default — the certresolver label
			// reappears on this dns01 host, and every PR orders its own certificate past the wildcard.
			// (That was a live bug: the comments promised inspect wired the seam at init, nothing did.)
			dir := t.TempDir()
			write(t, dir, "services:\n  app:\n    image: x\n    labels:\n      - pstack.routing.port=80\n")
			dns01Host := exec.NewFake(nil, "")
			dns01Host.Answer = func(cmd string) (exec.Result, bool) {
				if strings.HasPrefix(cmd, "docker ps") {
					return exec.Result{OK: true, Stdout: "abc123\n"}, true
				}
				if strings.HasPrefix(cmd, "docker inspect") {
					return exec.Result{OK: true, Stdout: `[{"Id":"abc123","Name":"/pstack-control-traefik-1","Config":{"Cmd":["--certificatesresolvers.le.acme.dnschallenge=true"]}}]`}, true
				}
				return exec.Result{}, false
			}
			r, err := MaterializeCompose(MaterializeArgs{Dir: dir, Spec: s(t), Runner: dns01Host})
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range generatedFor(t, r, "app") {
				if strings.Contains(l, "certresolver") {
					t.Errorf("the default seam did not consult the running traefik: %v", r.Generated)
				}
			}
		})
	})
}

func TestAugmentIsPureAndOrdered(t *testing.T) {
	// negative control: drop the Clone() in AugmentComposeDoc — the input gains labels.
	input := doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n")
	before := jsonOf(t, input)
	r := augment(t, input, s(t), HTTP01)
	if jsonOf(t, input) != before {
		t.Errorf("input mutated: %s", jsonOf(t, input))
	}
	// The user's labels come first, then the generated ones in their fixed order.
	labels := labelsOf(t, r.Doc, "app")
	want := []string{
		"pstack.routing.port=80",
		"traefik.enable=true",
		"traefik.docker.network=preview-ingress",
		"traefik.http.routers.app-pr-7.rule=Host(`app-pr-7.preview.example.com`)",
		"traefik.http.routers.app-pr-7.entrypoints=websecure",
		"traefik.http.routers.app-pr-7.tls=true",
		"traefik.http.routers.app-pr-7.service=app-pr-7",
		"traefik.http.services.app-pr-7.loadbalancer.server.port=80",
		"traefik.http.routers.app-pr-7.tls.certresolver=le",
	}
	if !reflect.DeepEqual(labels, want) {
		t.Errorf("order:\n%v\n%v", labels, want)
	}
	// Generated mirrors the appended part; the doc's keys keep the author's order.
	gen, _ := r.Generated.Get("app")
	if got := jsonOf(t, gen); got != jsonOf(t, want[1:]) {
		t.Errorf("generated: %s", got)
	}
	if !regexp.MustCompile(`^\{"services":\{"app":\{"image":"x","labels":\[.*\],"networks":\[`).MatchString(jsonOf(t, r.Doc)) {
		t.Errorf("key order: %s", jsonOf(t, r.Doc))
	}
}

func TestSwarmLabelsGoUnderDeploy(t *testing.T) {
	// negative control: write svc.labels under swarm too — deploy.labels is absent.
	b, _ := os.ReadFile("testdata/spec.yml")
	st, err := spec.Parse(string(b), map[string]string{"PR": "7", "PSTACK_ORCHESTRATOR": "swarm"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := augment(t, doc(t, "services:\n  app:\n    image: x\n    labels: [pstack.routing.port=80]\n"), st, HTTP01)
	app := r.Doc.GetMap("services").GetMap("app")
	dl := app.GetMap("deploy").GetSlice("labels")
	if len(dl) == 0 || dl[0] != "traefik.enable=true" || dl[1] != "traefik.swarm.network=preview-ingress" {
		t.Errorf("deploy.labels: %s", jsonOf(t, app))
	}
	if got := jsonOf(t, app.GetSlice("labels")); got != `["pstack.routing.port=80"]` {
		t.Errorf("container labels changed: %s", got)
	}
}
