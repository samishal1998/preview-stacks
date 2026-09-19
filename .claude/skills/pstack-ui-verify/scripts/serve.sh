#!/usr/bin/env bash
# A locally served pstack + the advanced UI (vite) + headless Chrome, against a fake docker.
#
#   serve.sh start [label] [arms...]   build, start all three, wait until each answers, print run.env
#   serve.sh stop  [label]             kill all three (whole process groups), drop chrome/, data/, the binary
#
# arms go to shim.ts (swarm loki grafana plugins; none = swarm loki plugins).
# Everything lives in $ROOT/<label> (label default: "default"): run.env, *.log, shim/ (docker +
# calls.log), shots/. $ROOT is durable on purpose — the session scratchpad and /tmp get wiped.
# Ports are free ones picked at start unless API_PORT / UI_PORT / CDP_PORT are set.
# The token and admin password are throwaway values for a loopback server, not credentials.
set -euo pipefail

ROOT="${PSTACK_UIV_ROOT:-$HOME/.claude/projects/-Volumes-S1-code-preview-stacks/ui-verify}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../../../.." && pwd)"
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
cmd="${1:-}"
label="${2:-default}"
shift $(($# < 2 ? $# : 2))
RUN="$ROOT/$label"

stop() {
  if [ ! -f "$RUN/pids" ]; then echo "nothing running for $label"; return 0; fi
  # Negative pid = the process group: vite's and Chrome's children die with them.
  while read -r pid; do kill -TERM -- "-$pid" 2>/dev/null || true; done <"$RUN/pids"
  sleep 1
  while read -r pid; do kill -KILL -- "-$pid" 2>/dev/null || true; done <"$RUN/pids"
  rm -rf "$RUN/pids" "$RUN/run.env" "$RUN/chrome" "$RUN/data" "$RUN/pstack"
  echo "stopped $label (logs, shim/ and shots/ kept in $RUN)"
}

wait_for() { # url logfile
  for _ in $(seq 150); do
    curl -sf -o /dev/null "$1" && return 0
    sleep 0.2
  done
  echo "timed out waiting for $1 — last lines of $RUN/$2:" >&2
  tail -20 "$RUN/$2" >&2
  return 1
}

start() {
  if [ -f "$RUN/pids" ]; then echo "$label is already running: serve.sh stop $label first" >&2; exit 1; fi
  [ -d "$REPO/apps/ui/node_modules" ] || { echo "run bun install at $REPO first" >&2; exit 1; }
  # Fresh data every run: the admin bootstrap only happens while no users exist.
  rm -rf "$RUN/data" "$RUN/chrome" "$RUN/shim"
  mkdir -p "$RUN/shots" "$RUN/data"
  trap '[ -f "$RUN/run.env" ] || { echo "start failed, cleaning up" >&2; stop; }' EXIT

  if [ -z "${API_PORT:-}${UI_PORT:-}${CDP_PORT:-}" ]; then
    # Three listeners open at once, so the three ports are distinct (harness/server.ts freePort).
    read -r API_PORT UI_PORT CDP_PORT < <(bun -e 'const s=[0,0,0].map(()=>Bun.serve({port:0,hostname:"127.0.0.1",fetch:()=>new Response("")}));console.log(s.map(x=>x.port).join(" "));s.forEach(x=>x.stop(true))')
  fi

  # Nothing inherited from the operator's shell: a real host's token must not reach this server, and
  # a stray PSTACK_IMPL makes the harness module shim.ts imports throw.
  for v in $(compgen -e | grep -E '^(PSTACK_|DOCKER_CONFIG$)' || true); do unset "$v"; done

  # Built here, not packages/pstack/bin: other agents run the conformance suite against that one.
  (cd "$REPO" && go build -o "$RUN/pstack" ./packages/pstack/cmd/pstack)
  bun "$HERE/shim.ts" "$RUN/shim" "$@"
  if [ $# -eq 0 ] || [[ " $* " == *" loki "* ]]; then
    # The Loki settings panel reads config.yaml from <data>/control/loki: seed the default render.
    mkdir -p "$RUN/data/control"
    cp -R "$REPO/packages/conformance/golden/render/control/http01-basic-compose-loki/loki" "$RUN/data/control/loki"
  fi

  export PSTACK_DATA="$RUN/data" PSTACK_HOST=127.0.0.1 PSTACK_PORT="$API_PORT" \
    PSTACK_TOKEN=uiv-local-token PSTACK_DOMAIN=preview.example.com \
    PSTACK_ADMIN_USER=admin PSTACK_ADMIN_PASSWORD=correct-horse PSTACK_LOKI_UID="$(id -u)"

  set -m # each background job gets its own process group, so stop can kill it whole
  PATH="$RUN/shim:$PATH" nohup "$RUN/pstack" serve >"$RUN/serve.log" 2>&1 </dev/null &
  echo $! >>"$RUN/pids"
  wait_for "http://127.0.0.1:$API_PORT/api/health" serve.log

  # localhost, not 127.0.0.1: cookies ignore ports, so this keeps the SPA's session apart from the
  # basic UI's on 127.0.0.1:$API_PORT. PSTACK_API is where vite proxies /api.
  (cd "$REPO/apps/ui" && PSTACK_API="http://127.0.0.1:$API_PORT" exec nohup bunx vite --port "$UI_PORT" --strictPort) >"$RUN/vite.log" 2>&1 </dev/null &
  echo $! >>"$RUN/pids"
  wait_for "http://localhost:$UI_PORT/" vite.log

  # Chrome last, so a reachable CDP port means everything is up. A fresh --user-data-dir, or it
  # hands off to the owner's running Chrome and no debug port appears.
  nohup "$CHROME" --headless=new --remote-debugging-port="$CDP_PORT" --user-data-dir="$RUN/chrome" \
    --no-first-run --no-default-browser-check about:blank >"$RUN/chrome.log" 2>&1 </dev/null &
  echo $! >>"$RUN/pids"
  wait_for "http://127.0.0.1:$CDP_PORT/json/version" chrome.log

  cat >"$RUN/run.env" <<EOF
LABEL=$label
RUN=$RUN
API=http://127.0.0.1:$API_PORT
SPA=http://localhost:$UI_PORT
CDP=http://127.0.0.1:$CDP_PORT
TOKEN=$PSTACK_TOKEN
ADMIN_USER=$PSTACK_ADMIN_USER
ADMIN_PASSWORD=$PSTACK_ADMIN_PASSWORD
SHOTS=$RUN/shots
EOF
  cat "$RUN/run.env"
}

case "$cmd" in
  start) start "$@" ;;
  stop) stop ;;
  *) echo "usage: serve.sh start [label] [swarm] [loki] [grafana] [plugins] | serve.sh stop [label]" >&2; exit 2 ;;
esac
