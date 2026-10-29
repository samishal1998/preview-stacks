# pstack-agent: prints one batch of node and container usage to stdout every 30 s. The control
# plane reads it back with `docker service logs --raw` (or `docker logs` on a compose host) and makes
# every decision; this script only reports. Runs as `sh -c` in a stock docker CLI image, so POSIX sh
# and POSIX awk only (busybox in the image, dash and mawk on CI): no bashisms, no gawk, no jq.
#
#   pstack-agent v1 node {"seq":N,"node":"<swarm node id|"">","hostname":…,"cores":…,"load1":…,
#     "load5":…,"memTotal":…,"memAvailable":…,"diskPath":…,"diskTotal":…,"diskFree":…}
#   pstack-agent v1 ctr {"seq":N,"node":…,"id":"<12-char id>","stack":…,"cpuPct":…,"mem":…}
#   pstack-agent v1 end {"seq":N,"node":…}
#
# - `node` is on every line: `service logs --raw` interleaves nodes line by line.
# - A cycle where any read fails prints nothing, so no `end` reaches the manager (unknown, never
#   pressure).
# - Memory is MemAvailable (page cache is not "used"); CPU is load average, divided by cores in the
#   manager; disk is `df -P /host<DockerRootDir>` (the host's `/` is mounted at /host).
# - `stack` is the stack namespace label, else the compose project label, else "". ctr lines follow
#   `docker ps` order, joined to `docker stats` on the short id.
# - Byte values print with %.0f, never %d (busybox's and older mawk's %d stop at 2^31) and never
#   through CONVFMT.
# - Strings are not JSON-escaped: docker constrains node ids and hostnames, and a batch the manager
#   cannot parse is dropped, which leaves the node unknown. `stack` is a label a compose file may set
#   to anything, so a ctr line whose labels hold `|` or whose stack holds `"` or `\` is not printed: it
#   could add keys or name another stack.
# - PSTACK_AGENT_ROOT prefixes /proc for tests; PSTACK_AGENT_ONCE=1 prints one batch and exits.

root=${PSTACK_AGENT_ROOT:-}
seq=1

cycle() {
	info=$(docker info --format '{{.Swarm.NodeID}}|{{.Name}}|{{.DockerRootDir}}') || return 1
	ps=$(docker ps --format '{{.ID}}|{{.Label "com.docker.stack.namespace"}}|{{.Label "com.docker.compose.project"}}') || return 1
	stats=$(docker stats --no-stream --format '{{.ID}}|{{.CPUPerc}}|{{.MemUsage}}') || return 1
	cores=$(nproc) || return 1
	mem=$(awk '$1 == "MemTotal:" { t = $2 } $1 == "MemAvailable:" { a = $2 }
		END { if (t == "" || a == "") exit 1; printf "%.0f|%.0f", t * 1024, a * 1024 }' "$root/proc/meminfo") || return 1
	load=$(awk 'NR == 1 && NF >= 2 { print ($1 + 0) "|" ($2 + 0); ok = 1 } END { if (!ok) exit 1 }' "$root/proc/loadavg") || return 1
	disk=$(df -P "/host${info##*|}" | awk 'NR == 2 { printf "%.0f|%.0f", $2 * 1024, $4 * 1024; ok = 1 }
		END { if (!ok) exit 1 }') || return 1
	printf '%s\n--\n%s\n' "$stats" "$ps" | awk -F'|' -v seq="$seq" -v info="$info" -v cores="$cores" \
		-v mem="$mem" -v la="$load" -v disk="$disk" '
	function bytes(s,   n, u) {
		if (!match(s, /^[0-9.]+/)) return 0
		n = substr(s, 1, RLENGTH); u = substr(s, RLENGTH + 1)
		if (u == "KiB") return n * 1024
		if (u == "MiB") return n * 1048576
		if (u == "GiB") return n * 1073741824
		if (u == "TiB") return n * 1099511627776
		if (u == "kB") return n * 1000
		if (u == "MB") return n * 1000000
		if (u == "GB") return n * 1000000000
		return n
	}
	BEGIN {
		split(info, i, "|"); split(mem, m, "|"); split(la, l, "|"); split(disk, d, "|")
		node = i[1]
		printf "pstack-agent v1 node {\"seq\":%s,\"node\":\"%s\",\"hostname\":\"%s\",\"cores\":%s,\"load1\":%s,\"load5\":%s,\"memTotal\":%s,\"memAvailable\":%s,\"diskPath\":\"%s\",\"diskTotal\":%s,\"diskFree\":%s}\n",
			seq, node, i[2], cores + 0, l[1], l[2], m[1], m[2], i[3], d[1], d[2]
	}
	!past { if ($0 == "--") past = 1; else if (NF >= 3) { cpu[$1] = $2; use[$1] = $3 }; next }
	NF == 3 && ($1 in cpu) {
		s = ($2 != "" ? $2 : $3)
		if (index(s, "\"") || index(s, "\\")) next
		c = cpu[$1]; sub(/%$/, "", c); split(use[$1], u, " ")
		printf "pstack-agent v1 ctr {\"seq\":%s,\"node\":\"%s\",\"id\":\"%s\",\"stack\":\"%s\",\"cpuPct\":%s,\"mem\":%.0f}\n",
			seq, node, $1, s, c + 0, bytes(u[1])
	}
	END { printf "pstack-agent v1 end {\"seq\":%s,\"node\":\"%s\"}\n", seq, node }'
}

while :; do
	cycle
	[ "${PSTACK_AGENT_ONCE:-}" = 1 ] && exit 0
	seq=$((seq + 1))
	sleep 30
done
