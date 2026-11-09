package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// ScalerConfig is the one scaler_config row. Credentials are stored secrets: only the root-only
// GET /api/scaler/config returns them. Report is the persisted part of the scaler's last report
// (JSON; empty before the first); SetScalerSettings never touches it.
type ScalerConfig struct {
	Preset                          string
	Vars                            map[string]string
	Min, Max, CooldownMin, GraceMin int
	Credentials                     map[string]string
	Report                          string
}

// GetScalerConfig reads the row; never nil. Unset → the defaults (0/3/5/15, empty maps), which
// match migration 12's column DEFAULTs.
func GetScalerConfig(q Querier) (*ScalerConfig, error) {
	c := ScalerConfig{Max: 3, CooldownMin: 5, GraceMin: 15}
	var vars, creds string
	err := q.QueryRow("SELECT preset, vars, min, max, cooldown_min, grace_min, credentials, report FROM scaler_config WHERE id = 1").
		Scan(&c.Preset, &vars, &c.Min, &c.Max, &c.CooldownMin, &c.GraceMin, &creds, &c.Report)
	if errors.Is(err, sql.ErrNoRows) {
		vars, creds = "{}", "{}"
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(vars), &c.Vars); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(creds), &c.Credentials); err != nil {
		return nil, err
	}
	// A stored `null` would unmarshal to nil.
	if c.Vars == nil {
		c.Vars = map[string]string{}
	}
	if c.Credentials == nil {
		c.Credentials = map[string]string{}
	}
	return &c, nil
}

// SetScalerSettings writes everything but Report, creating the row if absent.
func SetScalerSettings(q Querier, c ScalerConfig) error {
	vars, err := jsonObject(c.Vars)
	if err != nil {
		return err
	}
	creds, err := jsonObject(c.Credentials)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO scaler_config (id, preset, vars, min, max, cooldown_min, grace_min, credentials)
       VALUES (1, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(id) DO UPDATE SET preset = excluded.preset, vars = excluded.vars, min = excluded.min,
         max = excluded.max, cooldown_min = excluded.cooldown_min, grace_min = excluded.grace_min,
         credentials = excluded.credentials`,
		c.Preset, vars, c.Min, c.Max, c.CooldownMin, c.GraceMin, creds)
	return err
}

// SetScalerReport writes Report only, creating the row (with the column DEFAULTs) if absent.
func SetScalerReport(q Querier, report string) error {
	_, err := q.Exec(`INSERT INTO scaler_config (id, report) VALUES (1, ?)
       ON CONFLICT(id) DO UPDATE SET report = excluded.report`, report)
	return err
}

// ScalerConfigSet reports whether this host holds any scaler configuration: the scaler_config row
// or a custom preset. `pull config` names the scaler in Skipped only then.
func ScalerConfigSet(q Querier) (bool, error) {
	var set bool
	err := q.QueryRow("SELECT EXISTS(SELECT 1 FROM scaler_config) OR EXISTS(SELECT 1 FROM scaler_presets)").Scan(&set)
	return set, err
}

// ScalerPreset is one custom Tofu preset: file name → contents.
type ScalerPreset struct {
	Name      string
	Files     map[string]string
	UpdatedAt int64
}

// ListScalerPresets returns every custom preset, by name.
func ListScalerPresets(q Querier) ([]ScalerPreset, error) {
	rows, err := q.Query("SELECT name, files, updated_at FROM scaler_presets ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScalerPreset{}
	for rows.Next() {
		p, err := scanScalerPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetScalerPreset reads one custom preset; nil, nil when absent.
func GetScalerPreset(q Querier, name string) (*ScalerPreset, error) {
	p, err := scanScalerPreset(q.QueryRow("SELECT name, files, updated_at FROM scaler_presets WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// PutScalerPreset writes a custom preset, replacing one of the same name.
func PutScalerPreset(q Querier, p ScalerPreset) error {
	files, err := jsonObject(p.Files)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO scaler_presets (name, files, updated_at) VALUES (?, ?, ?)
       ON CONFLICT(name) DO UPDATE SET files = excluded.files, updated_at = excluded.updated_at`,
		p.Name, files, p.UpdatedAt)
	return err
}

// DeleteScalerPreset removes a custom preset; false when there was none.
func DeleteScalerPreset(q Querier, name string) (bool, error) {
	res, err := q.Exec("DELETE FROM scaler_presets WHERE name = ?", name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func scanScalerPreset(row interface{ Scan(...any) error }) (*ScalerPreset, error) {
	var p ScalerPreset
	var files string
	if err := row.Scan(&p.Name, &files, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(files), &p.Files); err != nil {
		return nil, err
	}
	if p.Files == nil {
		p.Files = map[string]string{}
	}
	return &p, nil
}

// jsonObject marshals m, writing a nil map as {} rather than null.
func jsonObject(m map[string]string) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}
