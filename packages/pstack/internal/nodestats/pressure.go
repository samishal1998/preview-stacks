// Pressure: when a node's newest fresh batch puts it in disk, memory or cpu pressure, and the line
// that says why. Pure, like the rest of the package: internal/api/pressure.go holds the state and
// raises the events.
//
//   - Enter: used ≥ DiskPct; MemAvailable ≤ MemPct of total; load5 ÷ cores ≥ CPULoad.
//   - Stay (the kind was held): the line moved by the recovery margin, 5 points for disk and memory
//     and 0.25 for cpu, so a value sitting on the line does not flap.
//   - A threshold of 0 is off: it neither enters nor holds the kind (NP-R1).
//
// Describe states the reading against the line, or, while only the margin holds the kind, where it
// clears. Memory is the free share, because MemAvailable is what is measured (NP-R22).
package nodestats

import (
	"math"
	"slices"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
)

type Kind string

const (
	Disk   Kind = "disk"
	Memory Kind = "memory"
	CPU    Kind = "cpu"
)

// Thresholds are the three pressure_* settings. 0 is off.
type Thresholds struct {
	DiskPct, MemPct int
	CPULoad         float64
}

// Reading is what pressure is judged on.
type Reading struct{ DiskUsedPct, MemFreePct, LoadPerCore float64 }

// Recovery margins.
const (
	pctMargin  = 5
	loadMargin = 0.25
)

// Read is false when a total it divides by is 0: no reading is unknown, never pressure.
func Read(n NodeLine) (Reading, bool) {
	if n.DiskTotal == 0 || n.MemTotal == 0 || n.Cores == 0 {
		return Reading{}, false
	}
	return Reading{
		DiskUsedPct: float64(n.DiskTotal-n.DiskFree) / float64(n.DiskTotal) * 100,
		MemFreePct:  float64(n.MemAvailable) / float64(n.MemTotal) * 100,
		LoadPerCore: n.Load5 / float64(n.Cores),
	}, true
}

// Evaluate is the kinds held now, given the kinds held before: disk, memory, cpu order, never nil.
func Evaluate(prev []Kind, r Reading, t Thresholds) []Kind {
	out := []Kind{}
	disk, mem := float64(t.DiskPct), float64(t.MemPct)
	if t.DiskPct > 0 && (r.DiskUsedPct >= disk || slices.Contains(prev, Disk) && r.DiskUsedPct >= disk-pctMargin) {
		out = append(out, Disk)
	}
	if t.MemPct > 0 && (r.MemFreePct <= mem || slices.Contains(prev, Memory) && r.MemFreePct <= mem+pctMargin) {
		out = append(out, Memory)
	}
	if t.CPULoad > 0 && (r.LoadPerCore >= t.CPULoad || slices.Contains(prev, CPU) && r.LoadPerCore >= t.CPULoad-loadMargin) {
		out = append(out, CPU)
	}
	return out
}

// Describe is one kind's line: `disk 94% ≥ 90%` at or past the line, `disk 87%, clears below 85%`
// while the margin holds it.
func Describe(k Kind, r Reading, t Thresholds) string {
	pct := func(f float64) string { return js.NumberString(math.Round(f)) + "%" }
	switch k {
	case Disk:
		if r.DiskUsedPct >= float64(t.DiskPct) {
			return "disk " + pct(r.DiskUsedPct) + " ≥ " + pct(float64(t.DiskPct))
		}
		return "disk " + pct(r.DiskUsedPct) + ", clears below " + pct(float64(t.DiskPct-pctMargin))
	case Memory:
		if r.MemFreePct <= float64(t.MemPct) {
			return "memory " + pct(r.MemFreePct) + " free ≤ " + pct(float64(t.MemPct))
		}
		return "memory " + pct(r.MemFreePct) + " free, clears above " + pct(float64(t.MemPct+pctMargin))
	default:
		load := "load " + js.NumberString(math.Round(r.LoadPerCore*10)/10) + " per core"
		if r.LoadPerCore >= t.CPULoad {
			return load + " ≥ " + js.NumberString(t.CPULoad)
		}
		// Rounded to cents: 1.1 − 0.25 is 0.8500000000000001 in float.
		return load + ", clears below " + js.NumberString(math.Round((t.CPULoad-loadMargin)*100)/100)
	}
}
