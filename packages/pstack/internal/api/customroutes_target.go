package api

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/autolabel"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// composeDoc is the submitted compose file of a resolved deployment with a compose block, parsed.
func composeDoc(st *spec.Stack, dir string) (*omap.Map, error) {
	raw, err := os.ReadFile(filepath.Join(dir, st.Compose.File))
	if err != nil {
		return nil, err
	}
	parsed, err := yamlx.Parse(raw)
	if err != nil {
		return nil, err
	}
	doc, ok := parsed.(*omap.Map)
	if !ok {
		return nil, &spec.Error{Msg: "compose file " + st.Compose.File + " must be a mapping"}
	}
	return doc, nil
}

// aliasTarget is what route `alias` mirrors of a resolved deployment (st from resolveDep, dir its
// deployment directory), plus every host the stack serves (plain, then `*.<host>` per wildcard,
// explicit ones included) for the collision check.
//
// It runs the deploy's own AugmentComposeDoc over the submitted compose file, never
// MaterializeCompose, which writes the derived file and asks docker. The challenge does not change
// a host. Only a generated `<name>-<stack>.<domain>` host is mirrored: an explicit
// pstack.routing.host or compose.subdomains `host:` is already stable, and a hand-labelled service
// never reaches Generated. An unreadable compose is an error, for the caller to log and skip.
func aliasTarget(st *spec.Stack, dir, alias string) (routing.AliasTarget, []string, error) {
	t := routing.AliasTarget{Alias: alias, Stack: st.Stack, Provider: "docker"}
	if orchestratorOf(st) == spec.Swarm {
		t.Provider = "swarm"
	}
	if st.Compose == nil {
		return t, nil, nil
	}
	doc, err := composeDoc(st, dir)
	if err != nil {
		return t, nil, err
	}
	aug, err := autolabel.AugmentComposeDoc(autolabel.AugmentArgs{Doc: doc, Spec: st, Challenge: autolabel.Unknown})
	if err != nil {
		return t, nil, err
	}
	// The domain AugmentComposeDoc built the generated hosts from.
	domain := spec.ResolvePreviewDomain(st.Env, st.DeclaredEnv, nil)
	hosts, wild := []string{}, []string{}
	aug.Generated.Each(func(_ string, v any) {
		id, host, hasWild := "", "", false
		for _, l := range v.([]string) {
			k, val, _ := strings.Cut(l, "=")
			router, isRule := strings.CutSuffix(strings.TrimPrefix(k, "traefik.http.routers."), ".rule")
			if !isRule {
				continue
			}
			if strings.HasSuffix(router, "-wild") {
				hasWild = true
			} else {
				id, host = router, strings.TrimSuffix(strings.TrimPrefix(val, "Host(`"), "`)")
			}
		}
		hosts = append(hosts, host)
		name := strings.TrimSuffix(id, "-"+st.Stack)
		svc := routing.AliasService{Name: name, Domain: domain}
		if hasWild {
			// Autolabel uses the first subdomain whose profile is the routing name.
			for _, sub := range st.Compose.Subdomains {
				if sub.Profile == name {
					wild = append(wild, "*."+sub.Host)
					if sub.Host == host {
						svc.Depth = sub.Depth
					}
					break
				}
			}
		}
		if domain != "" && host == id+"."+domain {
			t.Services = append(t.Services, svc)
		}
	})
	return t, append(hosts, wild...), nil
}
