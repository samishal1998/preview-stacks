feat(logging): Loki as a logging option (slice 1)

`pstack init --logging loki` (or `pstack logging loki|off` on an existing host) runs Loki in the control stack, installs the Grafana Loki Docker plugin on every node, and gives every deployed service without its own `logging:` block a Loki logging block labelled `service_name=<stack>-<service>`, pushed through Traefik to `loki.<domain>` with basic auth.

- Logging off renders the control stack byte-identically to today (the 8 render goldens prove it).
- The driver's retry/timeout options are fixed constants, so an unreachable Loki delays a container stop by seconds, not indefinitely.
- `loki.` is a reserved control hostname; previews cannot claim it.
- The compose file carrying the push password is written 0600; the plugin runs at `LOG_LEVEL=warn` so the password stays out of dockerd's log.
- Where the build differs from the spec is listed at the top of the design doc.

`bun run check` green; not yet run on a real host (the design doc has the checklist).
