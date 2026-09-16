package loki

import (
	"fmt"
	"strconv"
	"strings"

	pstack "github.com/samishal1998/preview-stacks/packages/pstack"
)

// Render is config.yaml for s.
func Render(s Settings) (string, error) { return render(pstack.LokiConfig, s) }

// The three S3 anchors: whole lines, because S3 inserts after them.
const (
	chunksDirLine  = "    directory: /loki/chunks\n"
	periodLine     = "        period: 24h                 # tsdb requires 24h\n"
	deleteStoreKey = "  delete_request_store: filesystem"
)

// render walks the anchors in order over template. Ordered, never a map (rule 5): the first missing
// anchor is the one named. See the package header.
func render(template string, s Settings) (string, error) {
	if err := check(s); err != nil {
		return "", err
	}
	days := strconv.Itoa(s.RetentionDays*24) + "h"
	aws, period, deleteStore := chunksDirLine, periodLine, deleteStoreKey
	if c := s.Storage.S3; c != nil { // check passed: type is s3
		host, insecure, _ := endpoint(c.Endpoint)
		aws = chunksDirLine +
			"  aws:\n" +
			"    endpoint: " + host + "\n" +
			"    region: " + c.Region + "\n" +
			"    bucketnames: " + c.Bucket + "\n" +
			fmt.Sprintf("%-36s# exact spelling: the field has no yaml tag\n", "    s3forcepathstyle: "+strconv.FormatBool(c.PathStyle)) +
			"    insecure: " + strconv.FormatBool(insecure) + "\n" +
			"    # No keys here: the SDK reads AWS_SHARED_CREDENTIALS_FILE, again on every restart.\n"
		period = periodLine +
			fmt.Sprintf("%-36s# S3 from here on; once this date passes it cannot be removed\n", `    - from: "`+c.Cutover+`"`) +
			"      store: tsdb\n" +
			"      object_store: s3\n" +
			"      schema: v13\n" +
			"      index:\n" +
			"        prefix: index_\n" +
			"        period: 24h\n"
		deleteStore = "  delete_request_store: s3"
	}
	for _, e := range []struct{ what, anchor, with string }{
		{"chunk_idle_period", "  chunk_idle_period: 30m", "  chunk_idle_period: " + dur(s.Chunks.IdlePeriodMinutes)},
		{"max_chunk_age", "  max_chunk_age: 2h", "  max_chunk_age: " + dur(s.Chunks.MaxAgeMinutes)},
		{"chunk_target_size", "  chunk_target_size: 1572864", "  chunk_target_size: " + strconv.Itoa(s.Chunks.TargetSizeKiB*1024)},
		{"chunk_encoding", "  chunk_encoding: snappy", "  chunk_encoding: " + s.Chunks.Encoding},
		{"query_ingesters_within", "  query_ingesters_within: 3h", "  query_ingesters_within: " + dur(s.Chunks.MaxAgeMinutes+60)},
		{"retention_period", "  retention_period: 168h", "  retention_period: " + days},
		{"max_query_lookback", "  max_query_lookback: 168h", "  max_query_lookback: " + days},
		{"chunks directory", chunksDirLine, aws},
		{"schema period", periodLine, period},
		{"delete_request_store", deleteStoreKey, deleteStore},
	} {
		if !strings.Contains(template, e.anchor) {
			return "", &Error{fmt.Sprintf("the loki config template has no %s (%q)", e.what, e.anchor)}
		}
		template = strings.Replace(template, e.anchor, e.with, 1)
	}
	return template, nil
}

// dur is m minutes in the template's spelling: whole hours as `2h`, anything else as `45m`.
func dur(m int) string {
	if m%60 == 0 {
		return strconv.Itoa(m/60) + "h"
	}
	return strconv.Itoa(m) + "m"
}
