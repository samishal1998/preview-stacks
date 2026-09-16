package loki

import (
	"strings"
	"testing"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

func TestRender(t *testing.T) {
	t.Run("the defaults render slice 1's file byte for byte", func(t *testing.T) {
		// negative control: in dur, return strconv.Itoa(m)+"m" for every m — `max_chunk_age: 120m` differs.
		got, err := Render(Defaults())
		if err != nil {
			t.Fatal(err)
		}
		if got != pstack.LokiConfig {
			t.Errorf("Render(Defaults()) is not pstack.LokiConfig:\n%s", got)
		}
	})

	t.Run("each field lands exactly once, comments kept", func(t *testing.T) {
		// negative control: drop the max_query_lookback entry from render's list — `max_query_lookback: 336h` appears 0 times.
		s := Defaults()
		s.RetentionDays = 14
		s.Chunks = Chunks{IdlePeriodMinutes: 45, MaxAgeMinutes: 180, TargetSizeKiB: 512, Encoding: "zstd"}
		got, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range []string{
			"  chunk_idle_period: 45m\n",
			"  max_chunk_age: 3h                 # must stay <= querier.query_ingesters_within\n",
			"  chunk_target_size: 524288\n",
			"  chunk_encoding: zstd\n",
			"  query_ingesters_within: 4h\n",
			"  retention_period: 336h            # 0s would keep forever\n",
			"  max_query_lookback: 336h\n",
			"  delete_request_store: filesystem  # required once retention is on\n",
		} {
			if n := strings.Count(got, line); n != 1 {
				t.Errorf("%q appears %d times", line, n)
			}
		}
		if _, err := yamlx.ParseString(got); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("minutes are hours only when whole; the query window is max age + 60m", func(t *testing.T) {
		// negative control: render query_ingesters_within from dur(MaxAgeMinutes) — `4h` becomes `3h`.
		for _, c := range []struct {
			maxAge      int
			age, window string
		}{{30, "30m", "90m"}, {90, "90m", "150m"}, {120, "2h", "3h"}, {180, "3h", "4h"}} {
			s := Defaults()
			s.Chunks.MaxAgeMinutes = c.maxAge
			got, err := Render(s)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, "  max_chunk_age: "+c.age+" ") || !strings.Contains(got, "  query_ingesters_within: "+c.window+"\n") {
				t.Errorf("maxAgeMinutes %d: want max_chunk_age %s, query_ingesters_within %s", c.maxAge, c.age, c.window)
			}
		}
	})

	t.Run("S3 adds the aws block, a second period and the s3 delete store", func(t *testing.T) {
		// negative control: leave deleteStore as deleteStoreKey on S3 — `delete_request_store` reads filesystem.
		got, err := Render(s3Settings())
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range []string{
			"    directory: /loki/chunks\n" +
				"  aws:\n" +
				"    endpoint: s3.eu-central-1.amazonaws.com\n" +
				"    region: eu-central-1\n" +
				"    bucketnames: pstack-logs\n" +
				"    s3forcepathstyle: false         # exact spelling: the field has no yaml tag\n" +
				"    insecure: false\n" +
				"    # No keys here: the SDK reads AWS_SHARED_CREDENTIALS_FILE, again on every restart.\n" +
				"\nschema_config:\n",
			"        period: 24h                 # tsdb requires 24h\n" +
				"    - from: \"2026-09-16\"            # S3 from here on; once this date passes it cannot be removed\n" +
				"      store: tsdb\n" +
				"      object_store: s3\n" +
				"      schema: v13\n" +
				"      index:\n" +
				"        prefix: index_\n" +
				"        period: 24h\n" +
				"\ningester:\n",
			"  delete_request_store: s3  # required once retention is on\n",
		} {
			if n := strings.Count(got, block); n != 1 {
				t.Errorf("%q appears %d times", block, n)
			}
		}
		v, err := yamlx.ParseString(got)
		if err != nil {
			t.Fatal(err)
		}
		cfg := v.(*omap.Map)
		configs := cfg.GetMap("schema_config").GetSlice("configs")
		if len(configs) != 2 {
			t.Fatalf("%d schema configs, want 2", len(configs))
		}
		first, _ := configs[0].(*omap.Map)
		second, _ := configs[1].(*omap.Map)
		if first.GetString("from") != "2024-04-01" || first.GetString("object_store") != "filesystem" ||
			second.GetString("from") != "2026-09-16" || second.GetString("object_store") != "s3" {
			t.Errorf("periods %v, %v", first, second)
		}
		if cfg.GetMap("compactor").GetString("delete_request_store") != "s3" {
			t.Error("delete_request_store is not s3")
		}
	})

	t.Run("path-style over http: the host, s3forcepathstyle true, insecure true", func(t *testing.T) {
		// negative control: render insecure as strconv.FormatBool(!insecure) — `insecure` reads false.
		s := s3Settings()
		s.Storage.S3.Endpoint, s.Storage.S3.PathStyle, s.Storage.S3.Bucket = "http://minio.internal:9000/", true, "logs.v1"
		got, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		v, err := yamlx.ParseString(got)
		if err != nil {
			t.Fatal(err)
		}
		aws := v.(*omap.Map).GetMap("storage_config").GetMap("aws")
		style, _ := aws.Get("s3forcepathstyle")
		insecure, _ := aws.Get("insecure")
		if aws.GetString("endpoint") != "minio.internal:9000" || aws.GetString("bucketnames") != "logs.v1" || style != true || insecure != true {
			t.Errorf("aws %v", aws)
		}
		if !strings.Contains(got, "    s3forcepathstyle: true          # exact spelling") {
			t.Error("the s3forcepathstyle comment is not at column 36")
		}
	})

	t.Run("config.yaml never holds a credential", func(t *testing.T) {
		// negative control: add `"    access_key_id: " + c.AccessKeyID + "\n"` to the aws block — the key id appears.
		got, err := Render(s3Settings())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(got, "access_key") || strings.Contains(got, "secret") {
			t.Errorf("config.yaml carries a credential:\n%s", got)
		}
	})

	t.Run("a value Validate refuses is never rendered", func(t *testing.T) {
		// negative control: drop the check call at the top of render — retentionDays 0 renders `retention_period: 0h`.
		zero := Defaults()
		zero.RetentionDays = 0
		injected := s3Settings()
		injected.Storage.S3.Bucket = "pstack-logs\n    access_key_id: x"
		for _, s := range []Settings{zero, injected} {
			if got, err := Render(s); !IsError(err) || got != "" {
				t.Errorf("got %q, %v", got, err)
			}
		}
	})

	for _, a := range []struct{ what, anchor string }{
		{"chunk_idle_period", "  chunk_idle_period: 30m"},
		{"max_chunk_age", "  max_chunk_age: 2h"},
		{"chunk_target_size", "  chunk_target_size: 1572864"},
		{"chunk_encoding", "  chunk_encoding: snappy"},
		{"query_ingesters_within", "  query_ingesters_within: 3h"},
		{"retention_period", "  retention_period: 168h"},
		{"max_query_lookback", "  max_query_lookback: 168h"},
		{"chunks directory", "    directory: /loki/chunks\n"},
		{"schema period", "        period: 24h                 # tsdb requires 24h\n"},
		{"delete_request_store", "  delete_request_store: filesystem"},
	} {
		t.Run("a template without "+a.what+" fails by name, on filesystem too", func(t *testing.T) {
			// negative control: skip the strings.Contains check in render — no error.
			_, err := render(strings.Replace(pstack.LokiConfig, a.anchor, "", 1), Defaults())
			if !IsError(err) || !strings.Contains(err.Error(), "has no "+a.what+" (") {
				t.Fatalf("got %v", err)
			}
		})
	}
}
