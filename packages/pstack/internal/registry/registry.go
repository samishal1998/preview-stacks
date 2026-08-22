// Package registry is the deployment registry — what turns pstack from a CLI into a control plane.
//
// The CLI acts on ONE spec file you point it at. The control plane instead holds MANY submitted
// deployments and acts on them by id, so a host can serve several projects and many PRs at once.
//
// Storage is a directory tree, not a database:
//
//	$PSTACK_DATA/deployments/<id>/
//	  spec.yml       the submitted spec
//	  compose.yml    the submitted compose file, when one was sent with it
//	  meta.json      { id, kind, createdAt, updatedAt, specName?, vars?, sleep? }
//
// Why no database: the registry is a cache of intent, never the source of truth about what exists.
// Truth lives in Docker and in each axis's own `assert_*` probe — the same rule the CLI follows.
// A lost registry means "I forgot what you asked for", not "the host is now inconsistent", and it
// is recoverable by re-submitting. A directory of YAML is also greppable, diffable, and trivially
// backed up, which a sqlite file is not.
//
// meta.json is read and written as an ORDERED DOCUMENT (*omap.Map), not as a struct: a record
// written by some other version of pstack carries keys this build never heard of, and a rewrite
// (a sleep) must hand them back byte-for-byte in their original position — the golden host pins
// it. The typed fields on DeploymentMeta are a decoded view of that document, never the source.
package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/exec"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/js"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/spec"
)

// DeploymentMeta is meta.json.
//
// Doc is the record exactly as stored — every key in file order, unknown keys included — and is what
// marshals. The other fields are read-only views decoded from it.
type DeploymentMeta struct {
	ID        string
	Kind      spec.Kind
	CreatedAt int64
	UpdatedAt int64
	// SpecName is the named spec this deployment uses, when it references one instead of carrying
	// its own copy. "" for an inline deployment.
	SpecName string
	// Vars are the variables STORED with the deployment.
	//
	// This is the fix for a genuine footgun: variables used to travel as `?query` params on every
	// call, so `up` with `PR=7` and a later `down` with `PR=8` (or none) tore down a DIFFERENT stack
	// and orphaned the first — the exact leak class this project exists to prevent. Storing them
	// makes the deployment self-describing: `down` resolves the same stack `up` created, always.
	//
	// Request-time variables still override these, so a one-off can still be forced, but nothing
	// REQUIRES the caller to remember.
	//
	// A map is fine here: it only ever feeds an env merge. The ORDERED copy (what a response shows)
	// is in Doc.
	Vars map[string]string
	// Sleep is present while the scheduler (or an operator) has the compose project asleep.
	//
	// This is the one record here that looks like state, so the line is worth drawing: it is NOT a
	// claim that nothing is running — docker answers that, and `up` clears it regardless. It is the
	// INTENT "wake this on a request" plus the only fact that cannot be recovered from docker once
	// the containers are gone: which hostnames the catch-all router should recognise as this
	// deployment's. Those are captured from the live labels the moment before teardown (inspect),
	// which is why a hand-written router is recognised exactly as a generated one is.
	Sleep *SleepRecord
	// Doc is the stored document: id, kind, createdAt, updatedAt, specName?, vars, sleep?, plus
	// whatever keys another version wrote, in file order. Marshal this, or Pairs().
	Doc *omap.Map
}

// MarshalJSON emits the stored document — `{...meta}` in the reference, file order and all.
func (m DeploymentMeta) MarshalJSON() ([]byte, error) { return m.Doc.MarshalJSON() }

// Pairs is the document as an ordered object a route can append its own fields to
// (`{ ...meta, asleep, orchestrator, … }`). Do not embed DeploymentMeta in a response struct: the
// promoted MarshalJSON would swallow the outer fields.
func (m DeploymentMeta) Pairs() jsonx.Object {
	out := jsonx.Object{}
	m.Doc.Each(func(k string, v any) { out = append(out, jsonx.KV{K: k, V: v}) })
	return out
}

// SleepRecord is the `sleep` record in meta.json.
type SleepRecord struct {
	Since int64 `json:"since"`
	// Reason is why — `idle 2h`, `after 3d`, or `operator` / the actor who asked.
	Reason string `json:"reason"`
	// Hosts are the exact hostnames from `Host(...)` rules.
	Hosts []string `json:"hosts"`
	// Rules are `HostRegexp(...)` patterns (wildcard subdomains), Go syntax — evaluated with JS RegExp.
	Rules []string `json:"rules"`
}

// Deployment is a stored deployment with its paths. Not a response type — the embedded
// MarshalJSON would emit the document alone, which is what `{...meta}` did; Dir and SpecPath
// never went over the wire.
type Deployment struct {
	DeploymentMeta
	Dir      string
	SpecPath string
}

// Error is the registry's own failure: a bad id, a rejected spec, no such deployment. The API maps
// it to 400/404 by its text.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// Ids become directory names and are echoed into shell hooks via the resolved stack, so restrict
// them to an alphabet with no traversal, no whitespace and no shell metacharacters. Rejecting here
// is the difference between a 400 and a path-traversal write.
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// idText is how the reference printed the regex object into the message — the JS literal.
const idText = `/^[a-z0-9][a-z0-9._-]{0,63}$/`

// AssertValidID rejects an id that could not be a directory name or a stack.
func AssertValidID(id string) error {
	if !idRe.MatchString(id) || strings.Contains(id, "..") {
		return &Error{fmt.Sprintf(`invalid deployment id "%s" — must match %s (lowercase, no traversal, no spaces)`, id, idText)}
	}
	return nil
}

// Registry is the deployment store. Owner: the server; every method is a read or a whole-file
// write, with no lock — the reference had none either (two concurrent writers lose one write there
// too). Stateless apart from Root and Env, so safe to share.
type Registry struct {
	Root string
	// Env is the ambient environment a stored deployment is resolved against — `process.env` in the
	// reference, read at call time. Nil means this process's environment; a test sets its own.
	Env map[string]string
}

// New returns the registry rooted at <dataDir>/deployments.
func New(dataDir string) *Registry {
	return &Registry{Root: filepath.Join(dataDir, "deployments")}
}

func (r *Registry) base() map[string]string {
	if r.Env != nil {
		return r.Env
	}
	return exec.Environ()
}

func (r *Registry) dir(id string) (string, error) {
	if err := AssertValidID(id); err != nil {
		return "", err
	}
	return filepath.Join(r.Root, id), nil
}

// List is every deployment with a readable meta.json, newest updatedAt first. A record that fails
// to read is skipped, never fatal: one broken directory must not blank the list.
func (r *Registry) List() ([]DeploymentMeta, error) {
	entries, err := os.ReadDir(r.Root)
	if err != nil {
		return []DeploymentMeta{}, nil // nothing submitted yet
	}
	out := []DeploymentMeta{}
	for _, e := range entries {
		meta, err := r.readMeta(e.Name())
		if err == nil {
			out = append(out, meta)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

// Get returns the deployment, or nil when there is no spec.yml under that id. An unreadable
// meta.json next to a present spec is an error, as it was.
func (r *Registry) Get(id string) (*Deployment, error) {
	dir, err := r.dir(id)
	if err != nil {
		return nil, err
	}
	specPath := filepath.Join(dir, "spec.yml")
	if _, err := os.Stat(specPath); err != nil {
		return nil, nil
	}
	meta, err := r.readMeta(id)
	if err != nil {
		return nil, err
	}
	return &Deployment{DeploymentMeta: meta, Dir: dir, SpecPath: specPath}, nil
}

// Source is the stored source: the spec, and the compose file when one was submitted with it (nil
// otherwise — absent whenever the submission had no compose section, not an error).
//
// WHY IT EXISTS. Without this the UI's "replace" form opened EMPTY, because nothing could hand back
// what was stored. Replacing is whole-record, so an operator re-typing a spec from memory silently
// dropped whatever they forgot — the axes stopped being tracked while the resources they created
// kept running. Being unable to read your own submission is what made that a likely mistake rather
// than a careless one.
//
// Restricted like a named spec's source, for the same reason: hook bodies are shell strings that
// routinely carry a credential inline. The caller decides; this only reads.
func (r *Registry) Source(id string) (specYaml string, compose *string, err error) {
	dep, err := r.Get(id)
	if err != nil {
		return "", nil, err
	}
	if dep == nil {
		return "", nil, &Error{"no such deployment: " + id}
	}
	b, err := os.ReadFile(dep.SpecPath)
	if err != nil {
		return "", nil, err
	}
	if c, err := os.ReadFile(filepath.Join(dep.Dir, "compose.yml")); err == nil {
		compose = jsonx.Str(string(c))
	}
	return string(b), compose, nil
}

// PutOptions are the extras a submission may carry.
type PutOptions struct {
	// ComposeYaml is written as compose.yml when non-nil.
	ComposeYaml *string
	// Env validates this submission only; it is never stored.
	Env map[string]string
	// Vars are recorded so `down` resolves the same stack `up` created. See DeploymentMeta.Vars.
	// Ordered, because the order a caller sent them in is the order meta.json and the response show.
	Vars *omap.Map
	// SpecName is set when this deployment references a named spec rather than owning its copy.
	SpecName *string
	// Host is the host-level ${vars.*}/${secrets.*}, so validation here matches resolution later.
	Host *spec.HostValues
}

// Put creates or replaces a deployment.
//
// The spec is parsed before anything is written, so a malformed submission is rejected without
// leaving a half-created deployment behind. `kind` is read from the parsed spec rather than taken
// from the caller — the file is the authority.
func (r *Registry) Put(id, specYaml string, opts PutOptions) (*Deployment, error) {
	dir, err := r.dir(id)
	if err != nil {
		return nil, err
	}
	// Read the existing record FIRST: validation below must see the merged variable set, or a
	// redeploy that supplies only `PR` fails on the `REGION` it was already created with — the
	// merge that preserves it happens further down, too late to help.
	var prev *DeploymentMeta
	if p, err := r.readMeta(id); err == nil {
		prev = &p
	}
	// { ...prev.vars, ...opts.vars }: the old keys keep their position, new ones append.
	mergedVars := omap.New()
	if prev != nil {
		if pv := prev.Doc.GetMap("vars"); pv != nil {
			mergedVars = pv.Clone()
		}
	}
	if opts.Vars != nil {
		opts.Vars.Each(func(k string, v any) { mergedVars.Set(k, v) })
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

	// Validate against the caller's variables; relative paths in the spec resolve from `dir`,
	// which is why compose.yml is written alongside it.
	// Same precedence as `resolve`: process env < stored vars < request env. Omitting `vars`
	// here made `put` reject the exact variables it was about to store.
	st, err := spec.Load(specPath, exec.Merge(r.base(), varsEnv(mergedVars), opts.Env), opts.Host)
	if err != nil {
		// Roll back rather than leave an unusable deployment on disk.
		_ = os.RemoveAll(dir)
		return nil, &Error{"spec rejected: " + err.Error()}
	}

	now := time.Now().UnixMilli()
	doc := omap.New()
	doc.Set("id", id)
	doc.Set("kind", string(st.Kind))
	createdAt := now
	if prev != nil {
		createdAt = prev.CreatedAt
	}
	doc.Set("createdAt", createdAt)
	doc.Set("updatedAt", now)
	// specName: opts.specName ?? prev?.specName — and JSON.stringify drops the key when undefined.
	switch {
	case opts.SpecName != nil:
		doc.Set("specName", *opts.SpecName)
	case prev != nil && prev.Doc.Has("specName"):
		v, _ := prev.Doc.Get("specName")
		doc.Set("specName", v)
	}
	// Already merged above, so validation and storage agree on exactly one variable set.
	doc.Set("vars", mergedVars)
	// A replace does not wake anything: the sleeping project is still the one these hostnames name.
	if prev != nil {
		if s, ok := prev.Doc.Get("sleep"); ok && s != nil {
			doc.Set("sleep", s)
		}
	}
	if err := writeDoc(filepath.Join(dir, "meta.json"), doc); err != nil {
		return nil, err
	}
	return &Deployment{DeploymentMeta: fromDoc(doc), Dir: dir, SpecPath: specPath}, nil
}

// SetSleep records that the project is asleep (and what hostnames wake it), or clears that record
// with nil. Rewrites meta.json in place; nothing else in the record changes, `updatedAt` included —
// a sleep is not an edit of what was asked for. A key this build does not know keeps its bytes and
// its position; a NEW `sleep` key lands after it, as a JS property assignment would.
func (r *Registry) SetSleep(id string, sleep *SleepRecord) error {
	meta, err := r.readMeta(id)
	if err != nil {
		return err
	}
	if sleep != nil {
		meta.Doc.Set("sleep", sleepDoc(sleep))
	} else {
		meta.Doc.Delete("sleep")
	}
	dir, _ := r.dir(id)
	return writeDoc(filepath.Join(dir, "meta.json"), meta.Doc)
}

// Remove forgets a deployment. Does NOT tear anything down — removing the record while containers
// still run would orphan them beyond the control plane's view, which is precisely the leak this
// project exists to prevent. Callers must `down` first; the API enforces that.
func (r *Registry) Remove(id string) error {
	dir, err := r.dir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Resolve turns a stored deployment into a Stack, interpolating with the given variables.
//
// Precedence: process env < stored vars < request vars. Stored vars beat the ambient environment so
// a stray PR=… in the server's own environment cannot hijack a deployment; request vars still win
// so a one-off override is possible. Host values live in their own ${vars.*}/${secrets.*}
// namespace and cannot collide with any of these.
func (r *Registry) Resolve(id string, env map[string]string, host *spec.HostValues) (*spec.Stack, error) {
	dep, err := r.Get(id)
	if err != nil {
		return nil, err
	}
	if dep == nil {
		return nil, &Error{"no such deployment: " + id}
	}
	return spec.Load(dep.SpecPath, exec.Merge(r.base(), dep.Vars, env), host)
}

func (r *Registry) readMeta(id string) (DeploymentMeta, error) {
	dir, err := r.dir(id)
	if err != nil {
		return DeploymentMeta{}, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return DeploymentMeta{}, err
	}
	v, err := omap.Parse(raw)
	if err != nil {
		return DeploymentMeta{}, err
	}
	doc, ok := v.(*omap.Map)
	if !ok {
		return DeploymentMeta{}, &Error{"meta.json is not an object"}
	}
	return fromDoc(doc), nil
}

// fromDoc decodes the typed view. Numbers arrive as int64 from omap; a hand-edited float is
// truncated, as `Number` arithmetic on the sort would have tolerated it.
func fromDoc(doc *omap.Map) DeploymentMeta {
	m := DeploymentMeta{
		ID:        doc.GetString("id"),
		Kind:      spec.Kind(doc.GetString("kind")),
		CreatedAt: int64Of(doc, "createdAt"),
		UpdatedAt: int64Of(doc, "updatedAt"),
		SpecName:  doc.GetString("specName"),
		Vars:      varsEnv(doc.GetMap("vars")),
		Doc:       doc,
	}
	if s := doc.GetMap("sleep"); s != nil {
		m.Sleep = &SleepRecord{
			Since:  int64Of(s, "since"),
			Reason: s.GetString("reason"),
			Hosts:  stringsOf(s.GetSlice("hosts")),
			Rules:  stringsOf(s.GetSlice("rules")),
		}
	}
	return m
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

// varsEnv is the stored vars as an env map. A non-string value (a hand edit) is coerced the way
// the reference's string interpolation would have, via String().
func varsEnv(m *omap.Map) map[string]string {
	out := map[string]string{}
	m.Each(func(k string, v any) { out[k] = js.ToString(v) })
	return out
}

func stringsOf(list []any) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, js.ToString(v))
	}
	return out
}

func anys(list []string) []any {
	out := make([]any, 0, len(list))
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// sleepDoc is the record in the document universe, in the field order the reference wrote.
func sleepDoc(s *SleepRecord) *omap.Map {
	return omap.From("since", s.Since, "reason", s.Reason, "hosts", anys(s.Hosts), "rules", anys(s.Rules))
}

// writeDoc is `writeFile(path, JSON.stringify(doc, null, 2), 'utf8')`: two-space indent, NO trailing
// newline, Node's default mode with the umask applied.
func writeDoc(path string, doc *omap.Map) error {
	b, err := jsonx.MarshalIndent(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o666)
}

// DataDir is where the control plane keeps its state. Overridable so tests never touch a real host.
// `??`: the variable's PRESENCE decides, so an empty PSTACK_DATA is an (unusable) empty path, as it was.
func DataDir() string {
	if v, ok := os.LookupEnv("PSTACK_DATA"); ok {
		return v
	}
	return "/var/lib/pstack"
}
