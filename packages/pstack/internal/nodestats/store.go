package nodestats

import "sort"

// StaleMs: a node with no new complete batch for this long is unknown, and unknown is never
// pressure (invariant 11).
const StaleMs = 120_000

// HistoryLen samples, one per 30 s batch: an hour of sparkline.
const HistoryLen = 120

// Sample is the part of a batch the sparklines draw.
type Sample struct {
	At           int64   `json:"at"`
	Load5        float64 `json:"load5"`
	MemAvailable int64   `json:"memAvailable"`
	DiskFree     int64   `json:"diskFree"`
}

// Entry is a node's newest batch, when its Raw was first seen, and its samples oldest first.
type Entry struct {
	Batch   Batch
	At      int64
	History []Sample
}

// Store keeps one Entry per node key. It has no mutex: its owner holds one (Server.statsMu).
type Store struct {
	entries map[string]*Entry
}

func NewStore() *Store { return &Store{entries: map[string]*Entry{}} }

// Record takes Parse's output. A batch whose Raw is already the node's newest is no news; a new one
// becomes the node's batch, stamped now, and adds a sample, dropping the oldest past HistoryLen.
func (s *Store) Record(now int64, batches []Batch) {
	for _, b := range batches {
		e := s.entries[b.Key]
		if e == nil {
			e = &Entry{History: []Sample{}}
			s.entries[b.Key] = e
		} else if e.Batch.Raw == b.Raw {
			continue
		}
		e.Batch, e.At = b, now
		e.History = append(e.History, Sample{At: now, Load5: b.Node.Load5, MemAvailable: b.Node.MemAvailable, DiskFree: b.Node.DiskFree})
		if over := len(e.History) - HistoryLen; over > 0 {
			e.History = e.History[over:]
		}
	}
}

// Get returns a copy: History and Ctrs are fresh slices, so a caller outside the lock cannot race
// Record.
func (s *Store) Get(key string) (Entry, bool) {
	e := s.entries[key]
	if e == nil {
		return Entry{}, false
	}
	return Entry{Batch: copyBatch(e.Batch), At: e.At, History: append([]Sample{}, e.History...)}, true
}

// Fresh returns the node's batch when it is no older than StaleMs.
func (s *Store) Fresh(key string, now int64) (Batch, bool) {
	e := s.entries[key]
	if e == nil || now-e.At > StaleMs {
		return Batch{}, false
	}
	return copyBatch(e.Batch), true
}

// Keys returns every node key the store has seen, sorted.
func (s *Store) Keys() []string {
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func copyBatch(b Batch) Batch {
	b.Ctrs = append([]CtrLine{}, b.Ctrs...)
	return b
}
