package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
)

// selfSigned is a wildcard pair for the domains, valid for a day either side of now.
func selfSigned(t *testing.T, domains ...string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, d := range domains {
		names = append(names, d, "*."+d)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

// The added domains' routers carry the TLS block of the mode, so a wildcard write re-renders them.
// negative control: drop the reconcileDomains() call in tlsWildcardPut → the added domain keeps
// `certResolver: le`; drop the one in tlsWildcardDelete → it keeps `tls: {}` with no certificate.
func TestWildcardWritesReRenderTheAddedDomains(t *testing.T) {
	s := grafanaServer(t)
	if _, err := s.routing.SetDomains([]string{"b.example"}, s.domainOptions()); err != nil {
		t.Fatal(err)
	}
	file := func() string {
		b, err := os.ReadFile(filepath.Join(s.routing.Dir, routing.DomainsYAML))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if !strings.Contains(file(), "certResolver: le") {
		t.Fatalf("http01 domains file has no resolver:\n%s", file())
	}

	call := func(method, body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/tls/wildcard", strings.NewReader(body))
		req.Header.Set("authorization", "Bearer t0ken")
		s.handle(rec, req)
		return rec.Code, rec.Body.String()
	}
	cert, key := selfSigned(t, "preview.example.com", "b.example")
	body, _ := json.Marshal(map[string]string{"cert": cert, "key": key})
	if code, out := call("PUT", string(body)); code != 200 {
		t.Fatalf("PUT: %d %s", code, out)
	}
	if got := file(); strings.Contains(got, "certResolver") || !strings.Contains(got, "tls: {}") {
		t.Fatalf("after PUT:\n%s", got)
	}

	if code, out := call("DELETE", ""); code != 200 {
		t.Fatalf("DELETE: %d %s", code, out)
	}
	if got := file(); !strings.Contains(got, "certResolver: le") {
		t.Fatalf("after DELETE:\n%s", got)
	}
}
