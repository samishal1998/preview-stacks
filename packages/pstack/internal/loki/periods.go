package loki

import (
	"slices"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

/*
 * The period guard. A schema period whose `from` has passed can never be removed or changed: data
 * written under it becomes unreadable. The row can lag or lead the files (a queued save, a stop
 * mid-apply, hand SQL), so every write of config.yaml — apply, rollback, resume — checks periods
 * read from FILES, never from the row. The caller picks `loaded` (config.yaml's, or
 * Render(previous)'s when a resumed apply's files were never loaded) and `adding` (true for an
 * apply and a resume, false for a rollback).
 */

// Period is one schema_config.configs entry. Compared with ==: any changed field is a changed period.
type Period struct {
	From, Store, ObjectStore, Schema, IndexPrefix, IndexPeriod string
}

// Periods reads schema_config.configs from a config.yaml text, in file order. `from` is quoted in
// every file pstack writes (templates/control/loki/config.yaml:22); a `from` that is not a string
// reads as "", which CheckPeriods treats as started.
func Periods(configYAML string) ([]Period, error) {
	none := &Error{Msg: "config.yaml has no schema periods"}
	v, err := yamlx.ParseString(configYAML)
	if err != nil {
		return nil, none
	}
	doc, _ := v.(*omap.Map) // the omap getters are nil-safe
	configs := doc.GetMap("schema_config").GetSlice("configs")
	if len(configs) == 0 {
		return nil, none
	}
	out := make([]Period, 0, len(configs))
	for _, c := range configs {
		m, ok := c.(*omap.Map)
		if !ok {
			return nil, none
		}
		out = append(out, Period{
			From:        m.GetString("from"),
			Store:       m.GetString("store"),
			ObjectStore: m.GetString("object_store"),
			Schema:      m.GetString("schema"),
			IndexPrefix: m.GetMap("index").GetString("prefix"),
			IndexPeriod: m.GetMap("index").GetString("period"),
		})
	}
	return out, nil
}

// CheckPeriods refuses writing `next` over `loaded`.
//
// Rule 1: every loaded period starting at or before now+lead stays in next, unchanged, at its index.
// Rule 2 (adding): every next period not in loaded starts after now+2×lead, so the swap, a failed
// ready wait and a whole rollback all finish before it. A rollback passes adding=false: a period
// that has not started stores nothing, so undoing a failed apply may drop it.
//
// A `from` that is not YYYY-MM-DD parses to the zero time, so it counts as started: live for
// rule 1, too close for rule 2.
func CheckPeriods(loaded, next []Period, now time.Time, lead time.Duration, adding bool) error {
	for i, p := range loaded {
		start, _ := time.Parse(time.DateOnly, p.From) // 00:00 UTC
		if !start.After(now.Add(lead)) && (i >= len(next) || next[i] != p) {
			return &Error{Msg: "config.yaml has a live schema period from " + p.From + " that this change would drop — not written"}
		}
	}
	if !adding {
		return nil
	}
	for _, p := range next {
		start, _ := time.Parse(time.DateOnly, p.From)
		if !slices.Contains(loaded, p) && !start.After(now.Add(2*lead)) {
			return &Error{Msg: "cutover " + p.From + " is too close — save again"}
		}
	}
	return nil
}
