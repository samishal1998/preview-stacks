package loki

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack"
)

// periodBase is the one period in pstack.LokiConfig; periodS3 is the one an S3 save appends.
var periodBase = Period{From: "2024-04-01", Store: "tsdb", ObjectStore: "filesystem", Schema: "v13", IndexPrefix: "index_", IndexPeriod: "24h"}

func periodS3(from string) Period {
	return Period{From: from, Store: "tsdb", ObjectStore: "s3", Schema: "v13", IndexPrefix: "index_", IndexPeriod: "24h"}
}

func periodClock(t *testing.T, rfc3339 string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func wantPeriodError(t *testing.T, err error, msg string) {
	t.Helper()
	if !IsError(err) || err.Error() != msg {
		t.Fatalf("err = %v, want *Error %q", err, msg)
	}
}

// negative control: see the subtests.
func TestPeriods(t *testing.T) {
	t.Run("the template has one period", func(t *testing.T) {
		// negative control: read IndexPrefix with m.GetString("prefix") instead of from the index map.
		got, err := Periods(pstack.LokiConfig)
		if err != nil {
			t.Fatal(err)
		}
		if want := []Period{periodBase}; !slices.Equal(got, want) {
			t.Fatalf("Periods(LokiConfig) = %+v, want %+v", got, want)
		}
	})

	t.Run("an S3 config has both periods, in file order", func(t *testing.T) {
		// negative control: range over configs[:1] instead of configs.
		const anchor = "        period: 24h                 # tsdb requires 24h\n"
		if !strings.Contains(pstack.LokiConfig, anchor) {
			t.Fatalf("the template has no %q", anchor)
		}
		s3 := strings.Replace(pstack.LokiConfig, anchor, anchor+
			"    - from: \"2026-09-16\"            # S3 from here on; once this date passes it cannot be removed\n"+
			"      store: tsdb\n"+
			"      object_store: s3\n"+
			"      schema: v13\n"+
			"      index:\n"+
			"        prefix: index_\n"+
			"        period: 24h\n", 1)
		got, err := Periods(s3)
		if err != nil {
			t.Fatal(err)
		}
		if want := []Period{periodBase, periodS3("2026-09-16")}; !slices.Equal(got, want) {
			t.Fatalf("Periods = %+v, want %+v", got, want)
		}
	})

	t.Run("no periods is an *Error", func(t *testing.T) {
		// negative control: drop the len(configs) == 0 check — "" and a config without schema_config read as no periods, no error.
		for _, text := range []string{
			"",
			"not: [yaml",
			"- a list\n",
			"auth_enabled: false\n",
			"schema_config:\n  configs: []\n",
			"schema_config:\n  configs:\n    - 2024\n",
		} {
			got, err := Periods(text)
			if got != nil {
				t.Errorf("Periods(%q) = %+v, want nil", text, got)
			}
			wantPeriodError(t, err, "config.yaml has no schema periods")
		}
	})
}

// negative control: see the subtests.
func TestCheckPeriods(t *testing.T) {
	const lead = 15 * time.Minute
	noon := periodClock(t, "2026-09-15T12:00:00Z")

	t.Run("dropping a started period is refused", func(t *testing.T) {
		// negative control: return nil before rule 1's loop.
		err := CheckPeriods([]Period{periodBase, periodS3("2026-09-15")}, []Period{periodBase}, noon, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2026-09-15 that this change would drop — not written")
	})

	t.Run("dropping a period that starts within lead is refused", func(t *testing.T) {
		// negative control: compare against now instead of now.Add(lead) — 2026-09-16 00:00 is 10m away, not yet started.
		late := periodClock(t, "2026-09-15T23:50:00Z")
		err := CheckPeriods([]Period{periodBase, periodS3("2026-09-16")}, []Period{periodBase}, late, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2026-09-16 that this change would drop — not written")
	})

	t.Run("a rollback may drop a period that starts after now+lead", func(t *testing.T) {
		// negative control: drop the start comparison, so every loaded period is live.
		if err := CheckPeriods([]Period{periodBase, periodS3("2026-09-16")}, []Period{periodBase}, noon, lead, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a changed live period is refused", func(t *testing.T) {
		// negative control: compare next[i].From != p.From instead of the whole Period.
		changed := periodBase
		changed.IndexPeriod = "168h"
		err := CheckPeriods([]Period{periodBase}, []Period{changed}, noon, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2024-04-01 that this change would drop — not written")
	})

	t.Run("a reordered live period is refused", func(t *testing.T) {
		// negative control: accept a live period anywhere in next (slices.Contains(next, p)) instead of at next[i].
		loaded := []Period{periodBase, periodS3("2026-09-01")}
		err := CheckPeriods(loaded, []Period{loaded[1], loaded[0]}, noon, lead, false)
		wantPeriodError(t, err, "config.yaml has a live schema period from 2024-04-01 that this change would drop — not written")
	})

	t.Run("adding a period that starts within 2×lead is refused", func(t *testing.T) {
		// negative control: compare against now.Add(lead) instead of now.Add(2*lead) — 00:00 is 20m away, past lead but within 2×lead.
		late := periodClock(t, "2026-09-15T23:40:00Z")
		err := CheckPeriods([]Period{periodBase}, []Period{periodBase, periodS3("2026-09-16")}, late, lead, true)
		wantPeriodError(t, err, "cutover 2026-09-16 is too close — save again")
	})

	t.Run("rule 2 only runs when adding", func(t *testing.T) {
		// negative control: run rule 2 whatever adding says.
		late := periodClock(t, "2026-09-15T23:40:00Z")
		if err := CheckPeriods([]Period{periodBase}, []Period{periodBase, periodS3("2026-09-16")}, late, lead, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("adding a period that starts after 2×lead is allowed", func(t *testing.T) {
		// negative control: refuse every period not in loaded, whatever its start.
		if err := CheckPeriods([]Period{periodBase}, []Period{periodBase, periodS3("2026-09-16")}, noon, lead, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a period already loaded is not re-checked by rule 2", func(t *testing.T) {
		// negative control: check every next period against 2×lead, not only the ones missing from loaded.
		late := periodClock(t, "2026-09-15T23:50:00Z")
		both := []Period{periodBase, periodS3("2026-09-16")}
		if err := CheckPeriods(both, both, late, lead, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a from that is not a date counts as started", func(t *testing.T) {
		// negative control: skip a period whose from does not parse (if err != nil { continue }) in both loops.
		bad := periodBase
		bad.From = "soon"
		wantPeriodError(t, CheckPeriods([]Period{bad}, []Period{}, noon, lead, false),
			"config.yaml has a live schema period from soon that this change would drop — not written")
		wantPeriodError(t, CheckPeriods([]Period{periodBase}, []Period{periodBase, bad}, noon, lead, true),
			"cutover soon is too close — save again")
	})
}
