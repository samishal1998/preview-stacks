// Package routing is Traefik dynamic configuration files — list, read, write, delete.
//
// WHAT THIS IS FOR. Traefik takes configuration from two providers: the **docker** provider (labels
// on containers — how every per-PR router is declared) and the **file** provider, which watches a
// directory and reloads on change. The file provider is where everything that is not a container
// lives: middleware (basicAuth, rate limits, IP allow-lists), TLS options, the catch-all fallback
// router, and routes to something running outside compose. Splitting it across several files is
// supported and is the point of a watched *directory* rather than one file — one file per concern,
// each independently editable.
//
// Until now that directory was created by `init` and then never touched again, so the only way to
// add a middleware was to SSH in. This module is the API behind doing it from the UI.
//
// ── WHY THIS NEEDS MORE CARE THAN IT LOOKS ───────────────────────────────────────────────────────
//
// Traefik's own documented behaviour: an unparseable file in the watched directory produces a parse
// error **for the directory**, and the rest of it can be discarded with it. The visible symptom is
// not "my new middleware is broken", it is *routes elsewhere disappearing*. So a careless write here
// does not break the thing you were editing — it breaks other people's previews.
//
// Three consequences, all load-bearing:
//
//  1. **Validate before touching disk.** Parse it, and require the top-level keys to be Traefik's.
//     A typo'd `htttp:` is valid YAML that configures nothing, and Traefik will not tell you.
//  2. **Write atomically.** A plain write truncates then fills, and the watcher can fire on the
//     truncated file. Write a temp file in the same directory and `rename` it — rename is atomic
//     within a filesystem, so the watcher only ever sees a whole file.
//  3. **Never put anything in that directory that is not dynamic config.** No `.bak`, no temp file
//     left behind, no dotfile — Traefik would try to parse it. This is why there is no on-disk
//     version history: the obvious place to keep it is the one place it must not go. `Write()`
//     returns the previous content instead, so a caller can offer an immediate revert.
//
// ── WHAT IT CANNOT BREAK ─────────────────────────────────────────────────────────────────────────
//
// `control.<domain>` and `api.<domain>` are declared as **docker labels on the pstack container**,
// not in this directory (see initctl). So no file written here can lock you out of the UI or the
// API that would let you fix it. That is not luck; keep it that way.
package routing

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/yamlx"
)

// Error is a refused name, refused content, an unwritable directory or a missing file. The API
// maps it to 400 with its own sentence.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// IsError reports whether err is a *Error.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// A filename that is safe as a path component AND that Traefik will actually read.
//
// `.yml`/`.yaml` only: the file provider reads YAML and TOML, but allowing both formats here would
// mean validating both, and one format is enough. No leading dot (a dotfile is still parsed, and is
// harder to notice), no directory separator, no `..`.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*\.ya?ml$`)

// sections are Traefik's top-level dynamic-configuration sections, in the order the message lists
// them. Anything else is a typo or a mistake.
var sections = []string{"http", "tcp", "udp", "tls"}

// AssertValidRoutingName rejects a filename Traefik would not read or that could leave the directory.
func AssertValidRoutingName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") {
		return &Error{fmt.Sprintf(`invalid filename "%s" — must be lowercase, end in .yml or .yaml, and contain no path separators (it becomes a file in Traefik's watched directory)`, name)}
	}
	return nil
}

// ValidateRoutingContent rejects a file that would break the directory, or silently do nothing.
//
// Returns the parsed mapping on success, so a caller does not parse twice.
//
// The "silently does nothing" half matters as much as the parse half: `htttp:` and `middlewares:`
// at the top level are both perfectly good YAML that Traefik ignores, and an ignored file is a
// support ticket that starts "I added the middleware and nothing happened".
func ValidateRoutingContent(content string) (*omap.Map, error) {
	if strings.TrimSpace(content) == "" {
		return nil, &Error{"the file is empty — delete it instead of storing an empty one"}
	}
	parsed, err := yamlx.ParseString(content)
	if err != nil {
		// Traefik's failure here is a parse error for the whole DIRECTORY, so this is the check that
		// stops one bad edit taking other routes down with it.
		return nil, &Error{"not valid YAML: " + strings.TrimPrefix(err.Error(), "not valid YAML: ")}
	}
	doc, ok := parsed.(*omap.Map)
	if !ok {
		return nil, &Error{"the top level must be a mapping of Traefik sections, e.g. `http:` with `routers:` under it"}
	}
	keys := doc.Keys()
	if len(keys) == 0 {
		return nil, &Error{"no sections — expected at least one of http, tcp, udp, tls"}
	}
	var unknown []string
	for _, k := range keys {
		if !isSection(k) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return nil, &Error{fmt.Sprintf("unknown top-level section(s): %s. Traefik reads only %s — it would load this file and silently apply nothing. Middlewares and routers go *under* `http:`.", strings.Join(unknown, ", "), strings.Join(sections, ", "))}
	}
	return doc, nil
}

func isSection(k string) bool {
	for _, s := range sections {
		if s == k {
			return true
		}
	}
	return false
}

// RoutingFile is one entry of the list view.
type RoutingFile struct {
	Name string `json:"name"`
	// Size in bytes. Enough for a list view to show something is there without reading every file.
	Size int64 `json:"size"`
	// UpdatedAt is epoch milliseconds — mtime.
	UpdatedAt int64 `json:"updatedAt"`
}

// RoutingStore is the watched directory, and whether this process can write to it.
//
// Writable is probed rather than assumed because the pstack container only gained this mount in
// 0.4.0: on a host whose control stack predates it the directory is simply absent from the
// container, and the API must say "re-run `pstack init`" rather than fail with ENOENT.
//
// Stateless apart from Dir; safe to share. No lock: two writers race on the rename, and the last
// whole file wins — the reference had the same property.
type RoutingStore struct {
	Dir string
	// wildcardMu serialises the wildcard's multi-file writes — see SetWildcard. It guards nothing
	// else: the single-file routes keep the last-whole-file-wins property described above.
	wildcardMu sync.Mutex
}

// New returns the store over dir.
func New(dir string) *RoutingStore { return &RoutingStore{Dir: dir} }

// Writable is true when the directory exists and this process may write in it.
func (s *RoutingStore) Writable() bool {
	st, err := os.Stat(s.Dir)
	if err != nil || !st.IsDir() {
		return false
	}
	// Probe by writing, not by reading a mode bit: the mount may be present but `:ro`, which is
	// exactly the misconfiguration worth detecting, and a mode check would not catch it.
	probe := filepath.Join(s.Dir, fmt.Sprintf(".pstack-write-probe-%d", os.Getpid()))
	if err := os.WriteFile(probe, nil, 0o666); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// List is every file Traefik would read, by name.
func (s *RoutingStore) List() []RoutingFile {
	files := []RoutingFile{}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		name := e.Name()
		if !nameRe.MatchString(name) {
			continue // never present a file Traefik would not read
		}
		st, err := os.Stat(filepath.Join(s.Dir, name))
		if err != nil {
			continue // vanished between readdir and stat — nothing to report
		}
		if st.Mode().IsRegular() {
			files = append(files, RoutingFile{Name: name, Size: st.Size(), UpdatedAt: st.ModTime().UnixMilli()})
		}
	}
	// Byte order where the reference used localeCompare — the same thing for the lowercase ASCII
	// the name rule admits.
	sort.SliceStable(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files
}

// Read returns a file's content.
func (s *RoutingStore) Read(name string) (string, error) {
	if err := AssertValidRoutingName(name); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return "", &Error{"no such routing file: " + name}
	}
	return string(b), nil
}

// Write validates, then replaces atomically. Returns the previous content, or nil if it is new.
//
// The temp file goes in the SAME directory because `rename` is only atomic within a filesystem —
// across one it degrades to copy-then-delete, which is the partial-file window this exists to
// avoid. It is named so that Traefik will not read it (no `.yml` suffix) and is removed on failure,
// because a leftover file in this directory is exactly what must never happen.
func (s *RoutingStore) Write(name, content string) (*string, error) {
	if err := assertNotReserved(name); err != nil {
		return nil, err
	}
	return s.write(name, content)
}

// write is Write without the reserved-name gate — for the routes that OWN a reserved file.
func (s *RoutingStore) write(name, content string) (*string, error) {
	if err := AssertValidRoutingName(name); err != nil {
		return nil, err
	}
	if _, err := ValidateRoutingContent(content); err != nil {
		return nil, err
	}
	if !s.Writable() {
		return nil, &Error{fmt.Sprintf("Traefik's dynamic directory is not writable from here (%s). The control stack mounts it into the API from 0.4.0 onward — re-run `pstack init` on the host to pick up the mount.", s.Dir)}
	}
	target := filepath.Join(s.Dir, name)
	var previous *string
	if b, err := os.ReadFile(target); err == nil {
		p := string(b)
		previous = &p
	}
	tmp := filepath.Join(s.Dir, fmt.Sprintf(".pstack-tmp-%d-%s.part", os.Getpid(), name))
	if err := os.WriteFile(tmp, []byte(content), 0o666); err != nil {
		_ = os.Remove(tmp)
		return nil, &Error{fmt.Sprintf("could not write %s: %s", name, err)}
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return nil, &Error{fmt.Sprintf("could not write %s: %s", name, err)}
	}
	return previous, nil
}

// Remove deletes a file and returns the removed content, so a caller can offer an undo.
func (s *RoutingStore) Remove(name string) (string, error) {
	if err := assertNotReserved(name); err != nil {
		return "", err
	}
	return s.remove(name)
}

// remove is Remove without the reserved-name gate — for the routes that OWN a reserved file.
func (s *RoutingStore) remove(name string) (string, error) {
	if err := AssertValidRoutingName(name); err != nil {
		return "", err
	}
	content, err := s.Read(name)
	if err != nil {
		return "", err
	}
	if !s.Writable() {
		return "", &Error{fmt.Sprintf("Traefik's dynamic directory is not writable from here (%s).", s.Dir)}
	}
	if err := os.Remove(filepath.Join(s.Dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return content, nil
}
