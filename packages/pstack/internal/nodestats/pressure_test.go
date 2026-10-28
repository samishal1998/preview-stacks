package nodestats

import (
	"reflect"
	"testing"
)

var defaults = Thresholds{DiskPct: 90, MemPct: 10, CPULoad: 2.0}

// negative control: drop the `t.X > 0` guard on the enter side → the three "threshold 0, not in
// prev" rows come back in pressure (NP-R1).
// negative control: drop the stay arm → the 85 / 15 / 1.75 rows clear.
func TestEvaluateHysteresis(t *testing.T) {
	// A reading that holds nothing, so each row moves one value.
	calm := Reading{DiskUsedPct: 50, MemFreePct: 50, LoadPerCore: 0.5}
	disk := func(v float64) Reading { r := calm; r.DiskUsedPct = v; return r }
	mem := func(v float64) Reading { r := calm; r.MemFreePct = v; return r }
	cpu := func(v float64) Reading { r := calm; r.LoadPerCore = v; return r }
	off := func(f func(*Thresholds)) Thresholds { t := defaults; f(&t); return t }
	cases := []struct {
		name string
		prev []Kind
		r    Reading
		t    Thresholds
		want []Kind
	}{
		{"disk 89.9 stays out", nil, disk(89.9), defaults, []Kind{}},
		{"disk 90 enters", nil, disk(90), defaults, []Kind{Disk}},
		{"disk 85 stays", []Kind{Disk}, disk(85), defaults, []Kind{Disk}},
		{"disk 85 does not enter", nil, disk(85), defaults, []Kind{}},
		{"disk 84.9 clears", []Kind{Disk}, disk(84.9), defaults, []Kind{}},
		{"memory 10 enters", nil, mem(10), defaults, []Kind{Memory}},
		{"memory 15 stays", []Kind{Memory}, mem(15), defaults, []Kind{Memory}},
		{"memory 15.1 clears", []Kind{Memory}, mem(15.1), defaults, []Kind{}},
		{"cpu 2.0 enters", nil, cpu(2.0), defaults, []Kind{CPU}},
		{"cpu 1.75 stays", []Kind{CPU}, cpu(1.75), defaults, []Kind{CPU}},
		{"cpu 1.74 clears", []Kind{CPU}, cpu(1.74), defaults, []Kind{}},
		{"disk threshold 0 in prev clears", []Kind{Disk}, disk(99), off(func(t *Thresholds) { t.DiskPct = 0 }), []Kind{}},
		{"memory threshold 0 in prev clears", []Kind{Memory}, mem(1), off(func(t *Thresholds) { t.MemPct = 0 }), []Kind{}},
		{"cpu threshold 0 in prev clears", []Kind{CPU}, cpu(9), off(func(t *Thresholds) { t.CPULoad = 0 }), []Kind{}},
		// NP-R1: a 0 is off on the enter side too, whatever the value.
		{"disk threshold 0 not in prev", nil, disk(99), off(func(t *Thresholds) { t.DiskPct = 0 }), []Kind{}},
		{"memory threshold 0 not in prev", nil, mem(0), off(func(t *Thresholds) { t.MemPct = 0 }), []Kind{}},
		{"cpu threshold 0 not in prev", nil, cpu(9), off(func(t *Thresholds) { t.CPULoad = 0 }), []Kind{}},
		{"all three, in order", nil, Reading{DiskUsedPct: 95, MemFreePct: 5, LoadPerCore: 3}, defaults, []Kind{Disk, Memory, CPU}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// negative control: return `out` without initialising it → the [] rows get nil.
			got := Evaluate(c.prev, c.r, c.t)
			if got == nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Evaluate(%v, %+v) = %#v, want %#v", c.prev, c.r, got, c.want)
			}
		})
	}
}

// negative control: print the stay line from the threshold instead of the margin → "clears below 90%".
// negative control: round load to an integer → "load 2 per core ≥ 2".
func TestDescribe(t *testing.T) {
	cases := map[string]string{
		"disk 94% ≥ 90%":                       Describe(Disk, Reading{DiskUsedPct: 94.2}, defaults),
		"memory 7% free ≤ 10%":                 Describe(Memory, Reading{MemFreePct: 6.8}, defaults),
		"load 2.4 per core ≥ 2":                Describe(CPU, Reading{LoadPerCore: 2.43}, defaults),
		"disk 87%, clears below 85%":           Describe(Disk, Reading{DiskUsedPct: 87.4}, defaults),
		"memory 12% free, clears above 15%":    Describe(Memory, Reading{MemFreePct: 12.3}, defaults),
		"load 1.9 per core, clears below 1.75": Describe(CPU, Reading{LoadPerCore: 1.91}, defaults),
		// A margin line never prints float noise: 1.1 − 0.25 is 0.8500000000000001.
		"load 1 per core, clears below 0.85": Describe(CPU, Reading{LoadPerCore: 1.0}, Thresholds{CPULoad: 1.1}),
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// negative control: skip the DiskTotal check → a 0-byte disk reads as NaN and Read says ok.
func TestRead(t *testing.T) {
	n := NodeLine{Cores: 4, Load5: 6, MemTotal: 1000, MemAvailable: 250, DiskTotal: 200, DiskFree: 20}
	r, ok := Read(n)
	if !ok || r != (Reading{DiskUsedPct: 90, MemFreePct: 25, LoadPerCore: 1.5}) {
		t.Fatalf("Read = %+v, %v", r, ok)
	}
	for _, zero := range []func(*NodeLine){
		func(n *NodeLine) { n.DiskTotal = 0 },
		func(n *NodeLine) { n.MemTotal = 0 },
		func(n *NodeLine) { n.Cores = 0 },
	} {
		m := n
		zero(&m)
		if _, ok := Read(m); ok {
			t.Errorf("Read(%+v) ok, want unknown", m)
		}
	}
}
