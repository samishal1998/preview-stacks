// Package specs is the named-spec store — store a spec once, reference it from many deployments.
//
// WHAT THIS FIXES. Before this, every deployment carried its own copy of the spec, so 50 open PRs
// meant 50 byte-identical YAML files and a fix to the teardown logic had to be re-submitted 50
// times. Worse, variables were not stored at all: `?PR=7` had to be repeated on every call, and
// passing a different value to `down` than to `up` silently tore down a *different stack* — the
// exact orphan class this project exists to prevent.
//
// So a deployment now has two possible shapes:
//
//	inline      { spec: "<yaml>" }              its own copy, as before — still valid, still useful
//	                                            for a one-off that shares nothing
//	referencing { specName: "web", vars: {…} }  points at a named spec; the vars are STORED
//
// Layout, alongside `deployments/`:
//
//	$PSTACK_DATA/specs/<name>/
//	  spec.yml       the spec source
//	  compose.yml    the compose file it references, when one was submitted with it
//	  meta.json      { name, kind, createdAt, updatedAt, description? }
//
// Same reasoning as the deployment registry: a directory of YAML, not a database. It is a cache of
// intent, never the source of truth about what exists — that stays in Docker and in each axis's own
// `assert_*` probe.
//
// Like the registry, meta.json is an ordered document (*omap.Map) with a typed view: the reference
// read it with a spread, so a key it never heard of came back in a list response in its place.
package specs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// SpecMeta is meta.json. Doc is the stored document and what marshals; the rest is a decoded view.
type SpecMeta struct {
	Name string
	// Kind is resolved from the stored source at write time, so a list view needs no re-parse.
	Kind        spec.Kind
	Description string // "" when absent
	CreatedAt   int64
	UpdatedAt   int64
	// RequiredVars are the variable names the spec interpolates but does not define — what a caller
	// MUST supply. Never nil.
	RequiredVars []string
	// Doc is name, kind, description?, createdAt, updatedAt, requiredVars, plus any key another
	// version wrote, in file order.
	Doc *omap.Map
}

// MarshalJSON emits the stored document, as `{...meta}` did.
func (m SpecMeta) MarshalJSON() ([]byte, error) { return m.Doc.MarshalJSON() }

// Pairs is the document as an ordered object a route can extend (`{ ...stored, source }`). Do not
// embed SpecMeta in a response struct: the promoted MarshalJSON would swallow the outer fields.
func (m SpecMeta) Pairs() jsonx.Object {
	out := jsonx.Object{}
	m.Doc.Each(func(k string, v any) { out = append(out, jsonx.KV{K: k, V: v}) })
	return out
}

// StoredSpec is a named spec with its paths.
type StoredSpec struct {
	SpecMeta
	Dir      string
	SpecPath string
}

// Error is the store's own failure: a bad name, a rejected spec, no such spec.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Same alphabet as a deployment id: becomes a directory name, so no traversal and no whitespace.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

const nameText = `/^[a-z0-9][a-z0-9._-]{0,63}$/`

// AssertValidSpecName rejects a name that could not be a directory name.
func AssertValidSpecName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") {
		return &Error{fmt.Sprintf(`invalid spec name "%s" — must match %s (lowercase, no traversal, no spaces)`, name, nameText)}
	}
	return nil
}

var (
	undefinedRe = regexp.MustCompile(`undefined variable\(s\) (.+?)\.`)
	varNameRe   = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// FindRequiredVars is which `${VAR}` references a spec cannot satisfy on its own.
//
// Parsing needs every variable defined, so this probes with a sentinel environment and collects the
// names the parser rejects, one round at a time. Reported up front, a caller learns "this spec needs
// PR and GIT_SHA" from a list view instead of discovering it from a 400 on deploy.
//
// Bounded: a spec whose own `env:` block is self-referential could otherwise loop. The result is
// sorted (`[...found].sort()` — byte order, which for identifier names is the same thing). The
// parser's error TEXT is scraped, which makes it a load-bearing internal contract.
func FindRequiredVars(source string) []string {
	found := map[string]bool{}
	sorted := func() []string {
		out := make([]string, 0, len(found))
		for k := range found {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for i := 0; i < 32; i++ {
		// Only the sentinels — NOT process.env. A variable that happens to exist in the server's
		// environment would otherwise be silently satisfied here and then be missing on a machine
		// where it does not, which is the worst kind of works-on-my-box.
		env := map[string]string{}
		for k := range found {
			env[k] = "x"
		}
		_, err := spec.Parse(source, env, nil)
		if err == nil {
			return sorted()
		}
		m := undefinedRe.FindStringSubmatch(err.Error())
		if m == nil {
			return sorted() // a different error — not our problem to report here
		}
		before := len(found)
		for _, n := range varNameRe.FindAllStringSubmatch(m[1], -1) {
			found[n[1]] = true
		}
		if len(found) == before {
			return sorted() // no progress; stop rather than spin
		}
	}
	return sorted()
}

// SpecStore is the named-spec store. Stateless apart from Root; safe to share.
type SpecStore struct {
	Root string
}

// New returns the store rooted at <dataDir>/specs.
func New(dataDir string) *SpecStore { return &SpecStore{Root: filepath.Join(dataDir, "specs")} }

func (s *SpecStore) dir(name string) (string, error) {
	if err := AssertValidSpecName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, name), nil
}

// List is every spec with a readable meta.json, newest updatedAt first.
func (s *SpecStore) List() ([]SpecMeta, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return []SpecMeta{}, nil
	}
	out := []SpecMeta{}
	for _, e := range entries {
		meta, err := s.readMeta(e.Name())
		if err == nil {
			out = append(out, meta)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// Get returns the spec, or nil when there is no spec.yml under that name.
func (s *SpecStore) Get(name string) (*StoredSpec, error) {
	dir, err := s.dir(name)
	if err != nil {
		return nil, err
	}
	specPath := filepath.Join(dir, "spec.yml")
	if _, err := os.Stat(specPath); err != nil {
		return nil, nil
	}
	meta, err := s.readMeta(name)
	if err != nil {
		return nil, err
	}
	return &StoredSpec{SpecMeta: meta, Dir: dir, SpecPath: specPath}, nil
}

// Source is the raw source, for showing or editing.
func (s *SpecStore) Source(name string) (string, error) {
	st, err := s.Get(name)
	if err != nil {
		return "", err
	}
	if st == nil {
		return "", &Error{"no such spec: " + name}
	}
	b, err := os.ReadFile(st.SpecPath)
	return string(b), err
}

// PutOptions are the extras a submission may carry.
type PutOptions struct {
	ComposeYaml *string
	// Description replaces the stored one when non-nil (`opts.description ?? prev?.description`).
	Description *string
}

// Put creates or replaces a named spec.
//
// Validated before anything is written, with sentinel values for whatever it interpolates — a
// named spec is a template, so it must be storable without knowing a particular PR's values.
func (s *SpecStore) Put(name, specYaml string, opts PutOptions) (*StoredSpec, error) {
	dir, err := s.dir(name)
	if err != nil {
		return nil, err
	}

	requiredVars := FindRequiredVars(specYaml)
	env := map[string]string{}
	for _, k := range requiredVars {
		env[k] = "x"
	}
	parsed, err := spec.Parse(specYaml, env, nil)
	if err != nil {
		// Nothing has touched disk yet, so there is no rollback to do — the failure is clean.
		return nil, &Error{"spec rejected: " + err.Error()}
	}

	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	specPath := filepath.Join(dir, "spec.yml")
	if err := os.WriteFile(specPath, []byte(specYaml), 0o666); err != nil {
		return nil, err
	}
	if opts.ComposeYaml != nil {
		if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(*opts.ComposeYaml), 0o666); err != nil {
			return nil, err
		}
	}

	now := time.Now().UnixMilli()
	var prev *SpecMeta
	if p, err := s.readMeta(name); err == nil {
		prev = &p
	}
	doc := omap.New()
	doc.Set("name", name)
	doc.Set("kind", string(parsed.Kind))
	// description: opts.description ?? prev?.description — the key is dropped when undefined.
	switch {
	case opts.Description != nil:
		doc.Set("description", *opts.Description)
	case prev != nil && prev.Doc.Has("description"):
		v, _ := prev.Doc.Get("description")
		doc.Set("description", v)
	}
	createdAt := now
	if prev != nil {
		createdAt = prev.CreatedAt
	}
	doc.Set("createdAt", createdAt)
	doc.Set("updatedAt", now)
	doc.Set("requiredVars", anys(requiredVars))
	b, err := jsonx.MarshalIndent(doc)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o666); err != nil {
		return nil, err
	}
	return &StoredSpec{SpecMeta: fromDoc(doc), Dir: dir, SpecPath: specPath}, nil
}

// Remove deletes a named spec.
//
// Callers MUST check for referencing deployments first — a dangling reference would leave a
// deployment that can never be torn down, since resolving it needs the spec. The API enforces it;
// this layer stays mechanical.
func (s *SpecStore) Remove(name string) error {
	dir, err := s.dir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func (s *SpecStore) readMeta(name string) (SpecMeta, error) {
	dir, err := s.dir(name)
	if err != nil {
		return SpecMeta{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return SpecMeta{}, err
	}
	v, err := omap.Parse(raw)
	if err != nil {
		return SpecMeta{}, err
	}
	doc, ok := v.(*omap.Map)
	if !ok {
		return SpecMeta{}, &Error{"meta.json is not an object"}
	}
	// Tolerate a record written before requiredVars existed: `{ ...m, requiredVars: m.requiredVars ?? [] }`
	// — a present key keeps its place, an absent one is appended.
	if rv, _ := doc.Get("requiredVars"); rv == nil {
		doc.Set("requiredVars", []any{})
	}
	return fromDoc(doc), nil
}

func fromDoc(doc *omap.Map) SpecMeta {
	rv := []string{}
	for _, v := range doc.GetSlice("requiredVars") {
		rv = append(rv, js.ToString(v))
	}
	return SpecMeta{
		Name:         doc.GetString("name"),
		Kind:         spec.Kind(doc.GetString("kind")),
		Description:  doc.GetString("description"),
		CreatedAt:    int64Of(doc, "createdAt"),
		UpdatedAt:    int64Of(doc, "updatedAt"),
		RequiredVars: rv,
		Doc:          doc,
	}
}

func int64Of(m *omap.Map, k string) int64 {
	switch x, _ := m.Get(k); v := x.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func anys(list []string) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}
