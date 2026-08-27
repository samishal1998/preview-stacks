package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/api"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/registry"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/version"
)

// ServeOptions are what the binary hands `serve`: the two embedded pages and the streams.
type ServeOptions struct {
	UIHTML    string
	ShareHTML string
	Stdout    io.Writer
	Stderr    io.Writer
	// Env is the process environment (os.LookupEnv-shaped); nil means the real one.
	Env func(string) (string, bool)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Serve is `pstack serve`. Blocks until SIGINT/SIGTERM, then stops the server.
func Serve(o ServeOptions) *Exit {
	env := o.Env
	if env == nil {
		env = os.LookupEnv
	}
	get := func(k string) string { v, _ := env(k); return v }
	token := get("PSTACK_TOKEN")
	port := 7878.0
	if v, ok := env("PSTACK_PORT"); ok {
		port = js.ParseNumber(v)
	}
	// Safety interlock: an unauthenticated instance of an API that can delete databases must not be
	// reachable off-box. Without a token we pin to loopback and say so.
	wantHost, ok := env("PSTACK_HOST")
	if !ok {
		wantHost = "127.0.0.1"
	}
	host := wantHost
	if token == "" {
		host = "127.0.0.1"
	}
	if token == "" && wantHost != "127.0.0.1" {
		return &Exit{Code: ExitUsage, Msg: "refusing to bind " + wantHost + " without PSTACK_TOKEN set — this API can destroy\n" +
			"        infrastructure. Set PSTACK_TOKEN=<secret> to listen off-loopback."}
	}
	dataDir := registry.DataDir()
	// In the control container Traefik's directory is mounted at /etc/traefik/dynamic. On the HOST
	// the same directory is at <dataDir>/control/traefik-dynamic. PSTACK_ROUTING_DIR overrides both.
	routingDir, ok := env("PSTACK_ROUTING_DIR")
	if !ok {
		routingDir = filepath.Join(dataDir, "control", "traefik-dynamic")
		if isDir("/etc/traefik/dynamic") {
			routingDir = "/etc/traefik/dynamic"
		}
	}
	registryDir, ok := env("DOCKER_CONFIG")
	if !ok {
		registryDir = filepath.Join(dataDir, "control", "docker")
		if isDir("/docker-config") {
			registryDir = "/docker-config"
		}
	}
	tuning := api.TuningFromEnv(env)
	srv, err := api.New(api.Options{
		DataDir:              dataDir,
		Port:                 int(port),
		Host:                 host,
		Token:                token,
		RoutingDir:           routingDir,
		RegistryDir:          registryDir,
		Domain:               get("PSTACK_DOMAIN"),
		MetricsURL:           get("PSTACK_TRAEFIK_METRICS"),
		ReadinessPollMs:      int64(tuning.ReadinessPollMs),
		ReadinessTimeoutMs:   int64(tuning.ReadinessTimeoutMs),
		ReadinessRestartLoop: int64(tuning.ReadinessRestartLoop),
		MaxJobs:              int(tuning.MaxJobs),
		// `off` and nothing else. A misspelling leaves the route ON, deliberately: this is a
		// convenience endpoint, and the alternative — any unrecognised value disabling it — turns a
		// typo into a CI pipeline that polls a 404 forever with nothing saying why.
		ProbeOff:         strings.EqualFold(strings.TrimSpace(get("PSTACK_PROBE")), "off"),
		SSOStateTTLS:     int64(tuning.SSOStateTTLS),
		SSODiscoveryTTLS: int64(tuning.SSODiscoveryTTLS),
		Version:          version.Get(),
		UIHTML:           o.UIHTML,
		ShareHTML:        o.ShareHTML,
		Log:              func(l string) { fmt.Fprintln(o.Stderr, l) },
	})
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	// First-admin bootstrap from the environment. Honoured only while no users exist (the Auth
	// layer enforces that), so a compose file carrying this pair cannot mint extra admins later.
	adminUser, adminPassword := get("PSTACK_ADMIN_USER"), get("PSTACK_ADMIN_PASSWORD")
	if adminUser != "" && adminPassword != "" {
		if st, err := store.Open(dataDir); err == nil {
			made, err := auth.New(st).Bootstrap(adminUser, adminPassword)
			switch {
			case err != nil && auth.IsError(err):
				// A bad username/password in the env must not take the API down.
				fmt.Fprintln(o.Stderr, "  ! admin bootstrap skipped: "+err.Error())
			case err != nil:
				_ = st.Close()
				return &Exit{Code: ExitFailed, Msg: err.Error()}
			case made != nil:
				fmt.Fprintln(o.Stdout, `  admin account "`+made.Username+`" created from PSTACK_ADMIN_USER`)
			}
			_ = st.Close()
		}
	}
	if err := srv.Start(); err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	fmt.Fprintln(o.Stdout, "pstack api  http://"+host+":"+js.NumberString(port))
	fmt.Fprintln(o.Stdout, "  registry: "+dataDir+"/deployments")
	if token != "" {
		fmt.Fprintln(o.Stdout, "  auth: auth required on every route (session, personal token, or the token below)")
	} else {
		fmt.Fprintln(o.Stdout, "  auth: NONE — bound to loopback only (set PSTACK_TOKEN to expose)")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	srv.Stop()
	return &Exit{Code: ExitOK}
}

// Healthcheck is the container's HEALTHCHECK: one GET against this process's own /api/health,
// exit 0 or 1. `init` blocks on this verdict, so it must be boring.
func Healthcheck(env func(string) (string, bool)) *Exit {
	if env == nil {
		env = os.LookupEnv
	}
	port := "7878"
	if v, ok := env("PSTACK_PORT"); ok {
		port = js.NumberString(js.ParseNumber(v))
	}
	c := &http.Client{Timeout: 4 * time.Second}
	r, err := c.Get("http://127.0.0.1:" + port + "/api/health")
	if err != nil {
		return &Exit{Code: ExitFailed}
	}
	defer r.Body.Close()
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		return &Exit{Code: ExitOK}
	}
	return &Exit{Code: ExitFailed}
}
