// Package nodestats holds the pure half of node pressure: reading pstack-agent's log lines into
// batches, remembering the newest batch and an hour of samples per node, and spelling the agent's
// docker commands (commands.go). It runs no command and takes no lock; init runs the create commands,
// and the api package owns the loop, the docker calls and the mutex.
//
// HOW A BATCH IS READ (templates/agent/agent.sh writes it):
//
//   - Only lines beginning `pstack-agent v1 ` count. Anything else, a docker log prefix included,
//     is skipped, so the manager reads `--raw` service logs.
//   - A batch is a `node` line, then the `ctr` lines with the same (node, seq), closed by the
//     matching `end`. Lines are matched on the line's own `node` field as well as `seq` (NP-R9):
//     two workers' lines interleave in service logs and often share a seq.
//   - A batch without its `end`, or a `ctr`/`end` without its `node`, is dropped: the `--since 90s`
//     window or a dying agent cut it, and the node keeps its previous complete batch.
//   - The newest batch is the LAST complete one in stream order, never the highest seq: a restarted
//     agent counts from 1 again.
//
// Raw, the node line's JSON text, is a batch's identity: the store treats a batch it has already
// seen as no news, so reading overlapping 90 s windows every 30 s records each batch once.
//
// Nothing here is persisted (invariant 10): a restart forgets the ring.
package nodestats

import (
	"encoding/json"
	"sort"
	"strings"
)

// NodeLine is one node's readings in a batch. Node is the swarm node id, "" on a compose host.
type NodeLine struct {
	Seq          int64   `json:"seq"`
	Node         string  `json:"node"`
	Hostname     string  `json:"hostname"`
	Cores        int     `json:"cores"`
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	MemTotal     int64   `json:"memTotal"`
	MemAvailable int64   `json:"memAvailable"`
	DiskPath     string  `json:"diskPath"`
	DiskTotal    int64   `json:"diskTotal"`
	DiskFree     int64   `json:"diskFree"`
}

// CtrLine is one container on that node. Stack is "" for a container no stack owns.
type CtrLine struct {
	Seq    int64   `json:"seq"`
	Node   string  `json:"node"`
	ID     string  `json:"id"`
	Stack  string  `json:"stack"`
	CPUPct float64 `json:"cpuPct"`
	Mem    int64   `json:"mem"`
}

// Batch is one complete report from one node. Raw is the node line's JSON text, the batch's identity.
type Batch struct {
	Key  string
	Raw  string
	Node NodeLine
	Ctrs []CtrLine
}

const prefix = "pstack-agent v1 "

// Key names a node: its swarm id, or its hostname on a compose host, where the id is "".
func Key(n NodeLine) string {
	if n.Node != "" {
		return n.Node
	}
	return n.Hostname
}

type open struct {
	node string
	seq  int64
}

// Parse returns the newest complete batch per Key, sorted by Key. Ctrs is never nil.
func Parse(logs string) []Batch {
	pending := map[open]*Batch{}
	newest := map[string]Batch{}
	for _, line := range strings.Split(logs, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), prefix)
		if !ok {
			continue
		}
		kind, raw, _ := strings.Cut(rest, " ")
		switch kind {
		case "node":
			var n NodeLine
			if json.Unmarshal([]byte(raw), &n) != nil {
				continue
			}
			// A second node line for the same (node, seq) is a restarted agent: the first one's
			// ctr lines belong to a batch that never ended.
			pending[open{n.Node, n.Seq}] = &Batch{Key: Key(n), Raw: raw, Node: n, Ctrs: []CtrLine{}}
		case "ctr":
			var c CtrLine
			if json.Unmarshal([]byte(raw), &c) != nil {
				continue
			}
			if b := pending[open{c.Node, c.Seq}]; b != nil {
				b.Ctrs = append(b.Ctrs, c)
			}
		case "end":
			var e struct {
				Seq  int64  `json:"seq"`
				Node string `json:"node"`
			}
			if json.Unmarshal([]byte(raw), &e) != nil {
				continue
			}
			k := open{e.Node, e.Seq}
			if b := pending[k]; b != nil {
				newest[b.Key] = *b
				delete(pending, k)
			}
		}
	}
	keys := make([]string, 0, len(newest))
	for k := range newest {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Batch, 0, len(keys))
	for _, k := range keys {
		out = append(out, newest[k])
	}
	return out
}
