package nodestats

import (
	"fmt"
	"reflect"
	"testing"
)

func batch(key string, seq int64) Batch {
	return Batch{
		Key:  key,
		Raw:  fmt.Sprintf(`{"seq":%d,"node":%q}`, seq, key),
		Node: NodeLine{Seq: seq, Node: key, Load5: float64(seq), MemAvailable: seq * 10, DiskFree: seq * 100},
		Ctrs: []CtrLine{{Seq: seq, Node: key, Stack: "pr-1"}},
	}
}

// negative control: set At = now on every Record, even for the same Raw → At reads 2000.
// negative control: Fresh uses now-At < StaleMs → the exact 120000 boundary reads stale.
// negative control: trim History to HistoryLen-1 → 119 samples.
func TestStoreRecordAndStale(t *testing.T) {
	s := NewStore()
	s.Record(1000, []Batch{batch("n1", 1)})
	s.Record(2000, []Batch{batch("n1", 1)})
	e, ok := s.Get("n1")
	if !ok || e.At != 1000 || len(e.History) != 1 {
		t.Fatalf("same Raw: %+v", e)
	}
	if want := (Sample{At: 1000, Load5: 1, MemAvailable: 10, DiskFree: 100}); e.History[0] != want {
		t.Errorf("sample = %+v", e.History[0])
	}
	if _, ok := s.Fresh("n1", 1000+StaleMs); !ok {
		t.Error("fresh at exactly StaleMs")
	}
	if _, ok := s.Fresh("n1", 1000+StaleMs+1); ok {
		t.Error("fresh past StaleMs")
	}
	if _, ok := s.Fresh("gone", 1000); ok {
		t.Error("fresh for an unknown key")
	}

	for i := int64(2); i <= 121; i++ {
		s.Record(i*1000, []Batch{batch("n1", i)})
	}
	e, _ = s.Get("n1")
	if len(e.History) != HistoryLen || e.History[0].At != 2000 || e.History[HistoryLen-1].At != 121000 {
		t.Fatalf("history len %d, first %d, last %d", len(e.History), e.History[0].At, e.History[len(e.History)-1].At)
	}
	if e.At != 121000 || e.Batch.Node.Seq != 121 {
		t.Errorf("entry = %d seq %d", e.At, e.Batch.Node.Seq)
	}
}

// Get hands out copies: a caller writing into them cannot reach the store (the owner reads them
// outside its lock).
// negative control: return the stored History slice as is → the second Get sees At 0.
// negative control: return the stored Ctrs slice as is → the second Get sees stack "x".
func TestStoreGetCopies(t *testing.T) {
	s := NewStore()
	s.Record(5, []Batch{batch("n1", 1)})
	e, _ := s.Get("n1")
	e.History[0].At = 0
	e.Batch.Ctrs[0].Stack = "x"
	again, _ := s.Get("n1")
	if again.History[0].At != 5 || again.Batch.Ctrs[0].Stack != "pr-1" {
		t.Fatalf("store mutated: %+v", again)
	}
	f, _ := s.Fresh("n1", 5)
	f.Ctrs[0].Stack = "y"
	again, _ = s.Get("n1")
	if again.Batch.Ctrs[0].Stack != "pr-1" {
		t.Fatalf("Fresh leaked: %+v", again)
	}
}

// negative control: return keys in map order → this flakes against the sorted want.
func TestStoreKeysSorted(t *testing.T) {
	s := NewStore()
	if k := s.Keys(); k == nil || len(k) != 0 {
		t.Fatalf("empty store keys = %#v", k)
	}
	s.Record(1, []Batch{batch("c", 1), batch("a", 1), batch("b", 1)})
	if k := s.Keys(); !reflect.DeepEqual(k, []string{"a", "b", "c"}) {
		t.Fatalf("keys = %v", k)
	}
}
