// The BRING-YOUR-OWN WILDCARD: one certificate pair, stored where Traefik reads and served to every
// preview by SNI — the `dns-persist-01` mode, minus (for now) the sidecar that would renew it.
//
// ── WHERE THE FILES LIVE, AND WHY THE ORDER OF WRITES IS LOAD-BEARING ────────────────────────────
//
// The PEMs go in `certs/` UNDER the watched directory: Traefik's file provider loads directories
// recursively but parses only .yml/.yaml/.toml, so a PEM there is readable by path yet can never be
// mistaken for configuration. The pointer to them is a top-level `tls-wildcard.yml` — and it is
// written LAST, every time, because the fsnotify watch is NOT recursive: an event in `certs/` wakes
// nobody, while the YAML landing at the top level triggers the rebuild that also RE-INLINES the
// certificate bytes (Traefik flattens certFile contents into the config at build time, and its
// unchanged-config check compares those bytes — so a byte-identical YAML still applies a rotated
// certificate). Removal inverts the order: the YAML goes first, so Traefik never holds a pointer to
// files that are already gone.
//
// ── WHAT WINS THE HANDSHAKE ───────────────────────────────────────────────────────────────────────
//
// File-provider certificates and ACME's share one store, and an exact-SAN certificate sorts before a
// wildcard — so previews that already own a per-hostname certificate keep serving it, and everything
// else inherits the wildcard. Nothing has to be revoked to adopt this mode.
//
// ── THE KEY HAS NO READ PATH ──────────────────────────────────────────────────────────────────────
//
// Invariant 15. The key goes in and never comes back out: WildcardInfo carries the certificate's
// public facts only, and no function returning key material exists in this package.
package routing

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WildcardYAML is the top-level dynamic file that points Traefik at the pair. Its presence IS the
// mode: `dns-persist-01` is detected by this file existing, never by a stored setting that could
// disagree with it.
const WildcardYAML = "tls-wildcard.yml"

// The pair, under the watched directory. Referenced from the YAML by the CONTAINER-side absolute
// path — Traefik resolves relative paths against its own working directory, which is not here.
const (
	wildcardCertRel = "certs/wildcard.crt"
	wildcardKeyRel  = "certs/wildcard.key"
	// traefikDynamicDir is where the control template mounts this directory in the TRAEFIK
	// container — the path the YAML must name, whatever this process sees the directory as.
	traefikDynamicDir = "/etc/traefik/dynamic"
)

// WildcardInfo is the certificate's public facts — never the key.
type WildcardInfo struct {
	Domains   []string `json:"domains"`
	NotBefore int64    `json:"notBefore"`
	NotAfter  int64    `json:"notAfter"`
	Issuer    string   `json:"issuer"`
	// SelfSigned is informational: fine for a lab, a browser warning in production.
	SelfSigned bool `json:"selfSigned"`
}

// IsReserved is true for a dynamic file THIS package owns rather than the operator.
//
// The gate exists because the mode is derived from the artifact: `tls-wildcard.yml` existing IS
// `dns-persist-01`, so a maintainer writing that one name through the generic routing API would
// have entered — or, deleting it, left — a mode whose own routes are admin's, and left the key pair
// orphaned in `certs/` on the way out. The routing API is for files the operator owns; this one
// belongs to /api/tls, which validates the pair the pointer names.
func IsReserved(name string) bool { return name == WildcardYAML }

func assertNotReserved(name string) error {
	if IsReserved(name) {
		return &Error{name + " is managed by pstack, not by hand: it points at the wildcard certificate pair stored beside it, and writing it here would name files that may not exist. Use PUT/DELETE /api/tls/wildcard (admin), which validates the pair and stores both halves."}
	}
	return nil
}

// WildcardActive is the mode probe: does the pointer file exist. Cheap enough for the deploy path.
func (s *RoutingStore) WildcardActive() bool {
	st, err := os.Stat(filepath.Join(s.Dir, WildcardYAML))
	return err == nil && st.Mode().IsRegular()
}

// WildcardInfo reads the stored certificate's public facts; nil when no wildcard is stored.
func (s *RoutingStore) WildcardInfo() *WildcardInfo {
	if !s.WildcardActive() {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, wildcardCertRel))
	if err != nil {
		return nil
	}
	leaf, err := leafOf(b)
	if err != nil {
		return nil
	}
	return infoOf(leaf)
}

func infoOf(leaf *x509.Certificate) *WildcardInfo {
	domains := append([]string{}, leaf.DNSNames...)
	if len(domains) == 0 && leaf.Subject.CommonName != "" {
		domains = append(domains, leaf.Subject.CommonName)
	}
	return &WildcardInfo{
		Domains:    domains,
		NotBefore:  leaf.NotBefore.UnixMilli(),
		NotAfter:   leaf.NotAfter.UnixMilli(),
		Issuer:     leaf.Issuer.CommonName,
		SelfSigned: leaf.Issuer.String() == leaf.Subject.String(),
	}
}

// leafOf parses the first certificate block of a PEM chain.
func leafOf(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, &Error{"the certificate is not PEM — expected a -----BEGIN CERTIFICATE----- block (the leaf first, any chain after it)"}
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, &Error{"the certificate does not parse: " + err.Error()}
	}
	return leaf, nil
}

// SetWildcard validates and stores the pair, then points Traefik at it. `domain` is the host's
// preview domain when known — the certificate must actually cover `*.<domain>`, because a wildcard
// for the wrong zone fails only at the visitor's browser, weeks later.
//
// Serialised: this is TWO renames plus the pointer, and interleaving them with another PUT would
// leave certA beside keyB — each half valid, the pair unloadable, and the failure visible only in
// Traefik's own log. That is precisely what the validation above exists to prevent.
func (s *RoutingStore) SetWildcard(certPEM, keyPEM, domain string) (*WildcardInfo, error) {
	s.wildcardMu.Lock()
	defer s.wildcardMu.Unlock()
	// The pair must MATCH — a cert stored with someone else's key serves nothing, and Traefik would
	// report it only in its own logs. tls.X509KeyPair is the whole check: parse, match, usable.
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return nil, &Error{"the certificate and key do not form a usable pair: " + err.Error()}
	}
	leaf, err := leafOf([]byte(certPEM))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return nil, &Error{fmt.Sprintf("the certificate expired %s — Traefik would load it and every preview would serve a browser error", leaf.NotAfter.UTC().Format("2006-01-02"))}
	}
	if now.Before(leaf.NotBefore) {
		return nil, &Error{fmt.Sprintf("the certificate is not valid until %s", leaf.NotBefore.UTC().Format("2006-01-02"))}
	}
	if domain != "" {
		// One representative preview hostname; covering it means covering them all, since every
		// generated hostname is exactly one label under the domain.
		if err := leaf.VerifyHostname("preview-probe." + domain); err != nil {
			return nil, &Error{fmt.Sprintf("the certificate does not cover *.%s — its names are %v. Previews live one label under the domain, so that wildcard is the one that matters", domain, leaf.DNSNames)}
		}
	}
	if !s.Writable() {
		return nil, &Error{fmt.Sprintf("Traefik's dynamic directory is not writable from here (%s). The control stack mounts it into the API from 0.4.0 onward — re-run `pstack init` on the host to pick up the mount.", s.Dir)}
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "certs"), 0o700); err != nil {
		return nil, &Error{"could not create the certs directory: " + err.Error()}
	}
	// Whether this is a first store or a rotation decides what a half-finished write must leave
	// behind — see the pointer-write failure path below.
	wasActive := s.WildcardActive()
	// PEMs first (atomically, 0600 — the key is a secret), the pointer YAML last: the YAML is the
	// watch event, and writing it re-inlines whatever bytes the PEMs hold at that moment.
	for _, f := range []struct{ rel, content string }{{wildcardCertRel, certPEM}, {wildcardKeyRel, keyPEM}} {
		target := filepath.Join(s.Dir, f.rel)
		tmp := filepath.Join(s.Dir, "certs", fmt.Sprintf(".pstack-tmp-%d-%s.part", os.Getpid(), filepath.Base(f.rel)))
		if err := os.WriteFile(tmp, []byte(f.content), 0o600); err != nil {
			_ = os.Remove(tmp)
			return nil, &Error{"could not write " + f.rel + ": " + err.Error()}
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return nil, &Error{"could not write " + f.rel + ": " + err.Error()}
		}
	}
	yaml := "# Written by pstack (PUT /api/tls/wildcard). The pair lives in certs/ beside this file;\n" +
		"# rewriting THIS file is what makes Traefik re-read it — edits by hand are honored but the\n" +
		"# next PUT replaces them wholesale.\n" +
		"tls:\n" +
		"  certificates:\n" +
		"    - certFile: " + traefikDynamicDir + "/" + wildcardCertRel + "\n" +
		"      keyFile: " + traefikDynamicDir + "/" + wildcardKeyRel + "\n"
	if _, err := s.write(WildcardYAML, yaml); err != nil {
		// The pair is on disk and nothing points at it. On a FIRST store that is a private key the
		// API would then deny holding — WildcardActive is false, so DELETE would answer "no wildcard
		// is stored" while the key sits in a directory mounted into Traefik. Take it back out.
		// On a ROTATION the previous pointer survives, so the pair must stay: it is what Traefik is
		// still serving.
		if !wasActive {
			for _, rel := range []string{wildcardCertRel, wildcardKeyRel} {
				_ = os.Remove(filepath.Join(s.Dir, rel))
			}
		}
		return nil, err
	}
	return infoOf(leaf), nil
}

// RemoveWildcard leaves the mode: pointer first, then the pair. Returns false when there was
// nothing to remove — neither pointer NOR pair.
//
// It does not short-circuit on the pointer alone. A crash between the two steps (or an operator who
// ignored the unlink error below) leaves a key with no pointer, and an early return would answer
// "no wildcard is stored" forever while it sits there, with no route left that would ever delete
// it. Every call sweeps both halves.
func (s *RoutingStore) RemoveWildcard() (bool, error) {
	s.wildcardMu.Lock()
	defer s.wildcardMu.Unlock()
	removed := false
	if s.WildcardActive() {
		if _, err := s.remove(WildcardYAML); err != nil {
			return false, err
		}
		removed = true
	}
	// The pointer is gone, so the mode is already left and Traefik already stopped serving the pair.
	// A key that will not unlink is still reported: "no wildcard is stored" while the private key
	// sits on disk is exactly the sentence an operator must not be told.
	for _, rel := range []string{wildcardCertRel, wildcardKeyRel} {
		err := os.Remove(filepath.Join(s.Dir, rel))
		switch {
		case err == nil:
			removed = true
		case !os.IsNotExist(err):
			return true, &Error{"the mode is off (Traefik no longer serves the wildcard) but " + rel + " could not be deleted: " + err.Error() + " — remove it on the host, it is a private key"}
		}
	}
	return removed, nil
}

// DynamicDir is where Traefik's dynamic configuration lives for THIS process — the resolution
// `pstack serve` has always performed, shared so the deploy path's mode probe agrees with the
// server: explicit override, then the in-container mount, then the host-side path under the data
// dir. (`??` semantics on the env: presence decides, not emptiness.)
func DynamicDir(dataDir string) string {
	if d, ok := os.LookupEnv("PSTACK_ROUTING_DIR"); ok {
		return d
	}
	if st, err := os.Stat(traefikDynamicDir); err == nil && st.IsDir() {
		return traefikDynamicDir
	}
	return filepath.Join(dataDir, "control", "traefik-dynamic")
}
