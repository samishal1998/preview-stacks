package nodestats

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The Task 3 script's own output, so the parser and the script cannot drift apart.
// negative control: unmarshal ctr lines into a struct whose CPUPct tag is "cpu" → CPUPct reads 0
// and the ctrs differ from the fixture.
func TestParseAgentOutput(t *testing.T) {
	got := Parse(read(t, "../../templates/agent/testdata/agent-output.txt"))
	if len(got) != 1 {
		t.Fatalf("want 1 batch, got %d", len(got))
	}
	b := got[0]
	node := "x8k2m9qv1n3p4r5s6t7u8w9y0"
	wantNode := NodeLine{Seq: 1, Node: node, Hostname: "worker-1", Cores: 8, Load1: 1.02, Load5: 1.1,
		MemTotal: 33399881728, MemAvailable: 4294967296, DiskPath: "/var/lib/docker",
		DiskTotal: 105152176128, DiskFree: 5136490496}
	wantCtrs := []CtrLine{
		{Seq: 1, Node: node, ID: "3f2a1b4c5d6e", Stack: "pr-42", CPUPct: 3.1, Mem: 129394278},
		{Seq: 1, Node: node, ID: "7a8b9c0d1e2f", Stack: "pr-7", CPUPct: 0.52, Mem: 1610612736},
		{Seq: 1, Node: node, ID: "0123456789ab", Stack: "", CPUPct: 0, Mem: 512000},
	}
	if b.Key != node {
		t.Errorf("Key = %q", b.Key)
	}
	if !reflect.DeepEqual(b.Node, wantNode) {
		t.Errorf("Node = %+v", b.Node)
	}
	if !reflect.DeepEqual(b.Ctrs, wantCtrs) {
		t.Errorf("Ctrs = %+v", b.Ctrs)
	}
	if !strings.HasPrefix(b.Raw, `{"seq":1,"node":"x8k2`) || !strings.HasSuffix(b.Raw, `"diskFree":5136490496}`) {
		t.Errorf("Raw = %q", b.Raw)
	}
}

// Spec review focus 1: a batch cut by the --since window at either end. n2's ctr/end at the top
// (its node line fell outside the window) and n1's trailing node+ctr with no end are both dropped,
// and n1 keeps its previous complete batch.
// negative control: publish a batch on the node line instead of on its end → seq 9 wins.
// negative control: let an end with no open batch publish an empty one → a second batch, for n2.
func TestParseDropsAnIncompleteBatch(t *testing.T) {
	got := Parse(read(t, "testdata/cut.txt"))
	if len(got) != 1 {
		t.Fatalf("want 1 batch, got %d", len(got))
	}
	if got[0].Node.Seq != 8 {
		t.Fatalf("want seq 8, got %d", got[0].Node.Seq)
	}
	want := []CtrLine{{Seq: 8, Node: "n1", ID: "cccccccccccc", Stack: "pr-8", CPUPct: 2.5, Mem: 200}}
	if !reflect.DeepEqual(got[0].Ctrs, want) {
		t.Errorf("Ctrs = %+v", got[0].Ctrs)
	}
}

// Spec review focus 2: an agent restarted inside the window starts again at seq 1.
// negative control: keep the batch with the highest seq per key → seq 900 wins.
func TestParseNewestIsStreamOrder(t *testing.T) {
	got := Parse(read(t, "testdata/restart.txt"))
	if len(got) != 1 || got[0].Node.Seq != 1 || got[0].Ctrs[0].Stack != "pr-new" {
		t.Fatalf("got %+v", got)
	}
}

// NP-R9: two workers' lines interleave in service logs, often with the same seq. Each ctr belongs
// to the batch of its own node.
// negative control: match ctr lines on seq only → both ctrs land in one batch.
func TestParseInterleavedNodes(t *testing.T) {
	logs := strings.Join([]string{
		`pstack-agent v1 node {"seq":3,"node":"nA","hostname":"a","cores":2,"load5":0.1,"diskFree":1}`,
		`pstack-agent v1 node {"seq":3,"node":"nB","hostname":"b","cores":2,"load5":0.2,"diskFree":2}`,
		`pstack-agent v1 ctr {"seq":3,"node":"nB","id":"bbbbbbbbbbbb","stack":"pr-b","cpuPct":1,"mem":2}`,
		`pstack-agent v1 ctr {"seq":3,"node":"nA","id":"aaaaaaaaaaaa","stack":"pr-a","cpuPct":1,"mem":1}`,
		`pstack-agent v1 end {"seq":3,"node":"nA"}`,
		`pstack-agent v1 end {"seq":3,"node":"nB"}`,
		`pstack-agent.1.xyz@b | pstack-agent v1 ctr {"seq":3,"node":"nB","id":"cccccccccccc"}`,
		`not an agent line`,
		``,
	}, "\n")
	got := Parse(logs)
	if len(got) != 2 || got[0].Key != "nA" || got[1].Key != "nB" {
		t.Fatalf("got %+v", got)
	}
	if len(got[0].Ctrs) != 1 || got[0].Ctrs[0].Stack != "pr-a" {
		t.Errorf("nA ctrs = %+v", got[0].Ctrs)
	}
	if len(got[1].Ctrs) != 1 || got[1].Ctrs[0].Stack != "pr-b" {
		t.Errorf("nB ctrs = %+v", got[1].Ctrs)
	}
}

// A compose host's agent has no swarm id: its key is the hostname, and a batch with no ctr lines
// still answers a non-nil slice (Go rule 3).
// negative control: Key returns n.Node unconditionally → Key is "".
// negative control: start a batch with a nil Ctrs → Ctrs is nil.
func TestParseComposeHostEmptyBatch(t *testing.T) {
	got := Parse("pstack-agent v1 node {\"seq\":1,\"node\":\"\",\"hostname\":\"box\"}\r\npstack-agent v1 end {\"seq\":1,\"node\":\"\"}\r\n")
	if len(got) != 1 || got[0].Key != "box" || got[0].Ctrs == nil {
		t.Fatalf("got %+v", got)
	}
}
