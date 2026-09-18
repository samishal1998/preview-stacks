// `/api/logging` — Loki's settings: retention and chunks, and where chunks are stored.
//
// THE ROW IS WHAT AN OPERATOR SAVED, NOT WHAT LOKI RUNS (invariant 10). Loki runs
// control/loki/config.yaml and its container. Once a save exists the API owns that file's content,
// the way it owns pstack-domains.yml; init only creates it. A hand edit is reverted by the next
// apply or pstack start, except that no live schema period is ever dropped (loki.CheckPeriods).
//
// A save is a `loki-apply` job on the pstack-control key (loki_apply.go): render, -verify-config,
// rename, restart Loki only, wait for ready, roll back when it does not come up. Nothing here
// renders compose or recreates a container (invariant 12); the mount and the env line are init's.
//
// ── A PUT, IN ORDER ─────────────────────────────────────────────────────────────────────────────
//
//  1. Loki here? Docker silent → 503; no `loki` container → 409. BEFORE the body is read, so a
//     bodiless PUT on a logging-off host is a 409.
//  2. No writable config.yaml in LokiDir → 409: pstack's ./loki mount comes from init.
//  3. Parse, then Merge over the stored row with any queued storage save on top. One-way and
//     fixed-field → 409 (sentinels, mapped here); a malformed or out-of-range field → 400 (fail()).
//  4. Storage with S3: pstack must be able to hand Loki's uid the 0600 credentials file → 409.
//  5. Nothing running or queued on the key, nothing pending, nothing changed → 200
//     {changed: false}. Otherwise the job decides, against the row it reads.
//  6. Storage with S3: the probe, before anything is recorded → 400 when S3 refuses.
//  7. Record the patch (lokiPut) and start the job → 202 {job}.
//
// Steps 3–4 are early refusals, not the authority: the job re-runs them against the row it reads.
//
// ── ROLES (permissions.go) ──────────────────────────────────────────────────────────────────────
//
// Retention and chunks are host configuration: maintainer, who can already restart Loki. Storage is
// ADMIN for BLAST RADIUS, the TLS-wildcard argument: one save sends every node's logs to an outside
// bucket, and it cannot be undone. Not an access boundary — viewers read logs through the logs
// routes. Two paths rather than a role check inside a handler, per the settings precedent. The
// `loki-apply` transcript is viewer-readable and may name the endpoint and bucket in Loki's error
// lines; only the secret and key id are scrubbed.
//
// The S3 secret has no read path (invariant 15). GET answers `secretSet`, never the mask; an empty,
// omitted or masked `secretAccessKey` keeps the stored one.
package api

import (
	"errors"
	"math"
	"net/http"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/auth"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/inspect"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/loki"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/terminal"
)

// loggingView is GET /api/logging. Field order is the JSON order.
type loggingView struct {
	// Enabled is tri-state: nil when docker did not answer.
	Enabled       *bool              `json:"enabled"`
	Source        string             `json:"source"`
	UpdatedAt     *int64             `json:"updatedAt"`
	RetentionDays int                `json:"retentionDays"`
	Chunks        loki.Chunks        `json:"chunks"`
	Storage       loggingStorageView `json:"storage"`
	Limits        loki.Limits        `json:"limits"`
}

type loggingStorageView struct {
	Type string         `json:"type"`
	S3   *loggingS3View `json:"s3"`
}

// loggingS3View is loki.S3 plus secretSet. There is no field that could carry the secret.
type loggingS3View struct {
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	PathStyle   bool   `json:"pathStyle"`
	AccessKeyID string `json:"accessKeyId"`
	SecretSet   bool   `json:"secretSet"`
	Cutover     string `json:"cutover"`
}

// lokiEnabled is GET's `enabled` and step 1 of both PUTs: nil when docker did not answer, else
// whether the control stack has a loki container. The lookup is the apply's own (lokiContainer).
func lokiEnabled(view inspect.ControlView) *bool {
	if !view.Reachable {
		return nil
	}
	_, on := lokiContainer(view)
	return &on
}

func (s *Server) loggingGet(w http.ResponseWriter) error {
	row, err := loki.Read(s.store)
	if err != nil {
		return err
	}
	v := loggingView{
		Enabled: lokiEnabled(inspect.ControlRuntime(s.host)),
		Source:  "default",
		Limits:  loki.LimitsAt(s.lokiNow(), s.lokiLead()),
	}
	set, secret := loki.Defaults(), ""
	if row != nil {
		set, secret = row.Settings, row.Secret
		v.Source = "db"
		v.UpdatedAt = &row.UpdatedAt
	}
	v.RetentionDays = set.RetentionDays
	v.Chunks = set.Chunks
	v.Storage.Type = set.Storage.Type
	if c := set.Storage.S3; c != nil {
		v.Storage.S3 = &loggingS3View{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, PathStyle: c.PathStyle,
			AccessKeyID: c.AccessKeyID, SecretSet: secret != "", Cutover: c.Cutover}
	}
	writeJSON(w, 200, v)
	return nil
}

func (s *Server) loggingPut(w http.ResponseWriter, r *http.Request, who *auth.Principal) error {
	return s.lokiSave(w, r, who, func(body *omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error) {
		c, err := parseChunks(body)
		return &c, nil, err
	})
}

func (s *Server) loggingStoragePut(w http.ResponseWriter, r *http.Request, who *auth.Principal) error {
	return s.lokiSave(w, r, who, func(body *omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error) {
		sp, err := parseStorage(body)
		return nil, &sp, err
	})
}

// lokiSave is both PUTs, in the order the file header gives.
func (s *Server) lokiSave(w http.ResponseWriter, r *http.Request, who *auth.Principal,
	parse func(*omap.Map) (*loki.ChunksPatch, *loki.StoragePatch, error)) error {
	// 1 and 2, before the body.
	on := lokiEnabled(inspect.ControlRuntime(s.host))
	if on == nil {
		writeError(w, 503, "docker did not answer")
		return nil
	}
	if !*on {
		writeError(w, 409, "Loki is not running on this host — run pstack logging loki on the host")
		return nil
	}
	if !loki.Writable(s.opts.LokiDir) {
		writeError(w, 409, "Loki's config.yaml is missing or read-only — run pstack upgrade on the host")
		return nil
	}

	// 3
	c, sp, err := parse(bodyOrEmpty(r))
	if err != nil {
		return err
	}
	row, err := loki.Read(s.store)
	if err != nil {
		return err
	}
	lead := s.lokiLead()
	base := row
	s.lokiMu.Lock()
	var queued *loki.StoragePatch
	if s.lokiStorage != nil {
		queued = s.lokiStorage.storage
	}
	s.lokiMu.Unlock()
	if queued != nil {
		// A storage save no job has taken yet. The fixed-field rules check against it. One that no
		// longer merges (its cutover came too close) is its job's to refuse, not this request's.
		if set, secret, err := loki.Merge(row, nil, queued, lead); err == nil {
			base = &loki.Row{Settings: set, Secret: secret}
		}
	}
	merged, secret, err := loki.Merge(base, c, sp, lead)
	if errors.Is(err, loki.ErrOneWay) || errors.Is(err, loki.ErrFixed) {
		writeError(w, 409, err.Error())
		return nil
	}
	if err != nil {
		return err
	}

	// 4
	s3 := sp != nil && merged.Storage.S3 != nil
	if s3 {
		if err := loki.CredentialsOwner(s.opts.LokiUID); err != nil {
			writeError(w, 409, err.Error())
			return nil
		}
	}

	// 5
	if !s.jobs.IsBusy(inspect.ControlProject) && !s.lokiPending() && len(loki.Changed(row, merged, secret)) == 0 {
		writeJSON(w, 200, jsonx.O("changed", false))
		return nil
	}

	// 6
	if s3 {
		if err := loki.Probe(r.Context(), *merged.Storage.S3, secret); err != nil {
			return err
		}
	}

	// 7
	by := terminal.ActorOf(*who)
	job, ok := s.startLokiApply(s.lokiPut(by, c, sp), by, false)
	if !ok {
		// A waiting teardown of this key outranks the apply (jobs.go:597-605). The patch stays
		// pending; the next save's job carries it.
		writeError(w, 409, "pstack-control is busy with a teardown — retry")
		return nil
	}
	writeJSON(w, 202, jsonx.O("job", job.Stub()))
	return nil
}

// parseChunks is PUT /api/logging's body. Every number is required. A missing encoding is left ""
// for loki.Validate to name.
func parseChunks(body *omap.Map) (loki.ChunksPatch, error) {
	var p loki.ChunksPatch
	chunks := body.GetMap("chunks")
	for _, f := range []struct {
		m         *omap.Map
		key, name string
		into      *int
	}{
		{body, "retentionDays", "retentionDays", &p.RetentionDays},
		{chunks, "idlePeriodMinutes", "chunks.idlePeriodMinutes", &p.Chunks.IdlePeriodMinutes},
		{chunks, "maxAgeMinutes", "chunks.maxAgeMinutes", &p.Chunks.MaxAgeMinutes},
		{chunks, "targetSizeKiB", "chunks.targetSizeKiB", &p.Chunks.TargetSizeKiB},
	} {
		n, err := wholeNumber(f.m, f.key, f.name)
		if err != nil {
			return p, err
		}
		*f.into = n
	}
	p.Chunks.Encoding = chunks.GetString("encoding")
	return p, nil
}

// parseStorage is PUT /api/logging/storage's body. `{type: filesystem}` needs nothing else. For S3,
// a missing string is left "" for loki.Validate to name.
func parseStorage(body *omap.Map) (loki.StoragePatch, error) {
	var p loki.StoragePatch
	switch body.GetString("type") {
	case loki.StorageFilesystem:
		p.Storage.Type = loki.StorageFilesystem
		return p, nil
	case loki.StorageS3:
	default:
		return p, &loki.Error{Msg: "type must be filesystem or s3"}
	}
	pathStyle, ok := getBool(body, "pathStyle")
	if !ok {
		return p, &loki.Error{Msg: "pathStyle must be true or false"}
	}
	p.Storage = loki.Storage{Type: loki.StorageS3, S3: &loki.S3{
		Endpoint:    body.GetString("endpoint"),
		Region:      body.GetString("region"),
		Bucket:      body.GetString("bucket"),
		PathStyle:   pathStyle,
		AccessKeyID: body.GetString("accessKeyId"),
		Cutover:     body.GetString("cutover"),
	}}
	// The SSO convention: the mask round-tripped, or nothing typed, means keep the stored secret.
	if secret := body.GetString("secretAccessKey"); secret == "" || secret == secretMask {
		p.KeepSecret = true
	} else {
		p.Secret = secret
	}
	return p, nil
}

// wholeNumber is a required integer field: JSON 14 or 14.0, never 14.5 or "14".
func wholeNumber(m *omap.Map, key, name string) (int, error) {
	v, _ := m.Get(key)
	switch n := v.(type) {
	case int64:
		return int(n), nil
	case float64:
		if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
			return int(n), nil
		}
	}
	return 0, &loki.Error{Msg: name + " must be a whole number"}
}
