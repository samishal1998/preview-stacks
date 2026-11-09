package cli

import (
	"context"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/scaler"
)

// `pstack scaler` — the worker scaler daemon, the control stack's `scaler` container. Like serve,
// it reads only the environment (W-R25): the compose file sets it, and the token is a secret.
func scalerCmd(args *Parsed, ios IO) *Exit {
	if args.Sub != "" {
		return fail("usage: pstack scaler")
	}
	token, _ := ios.Env("PSTACK_TOKEN")
	if token == "" {
		return fail("set PSTACK_TOKEN — the host's root token")
	}
	base := envOr(ios.Env, "PSTACK_API_URL", "http://pstack:7878")
	dir := envOr(ios.Env, "PSTACK_SCALER_STATE", "/state")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	err := scaler.Run(ctx, scaler.RunOptions{
		API:      scaler.Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}},
		StateDir: dir,
		Log:      ios.Stdout,
	})
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	return nil
}

// envOr is v, or def when it is unset or empty.
func envOr(env func(string) (string, bool), k, def string) string {
	if v, _ := env(k); v != "" {
		return v
	}
	return def
}
