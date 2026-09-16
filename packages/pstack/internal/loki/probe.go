package loki

// ── THE PROBE ────────────────────────────────────────────────────────────────────────────────────
//
// -verify-config builds no storage client, and before the cutover Loki writes nothing to S3, so a
// wrong bucket would first surface at the first flush after midnight UTC, with nobody watching.
// Probe runs inside PUT /api/logging/storage instead: a SigV4-signed PUT of an empty object, then a
// DELETE of it (the compactor deletes chunks for retention). That proves the endpoint, TLS, the
// region, the bucket, the credentials, and write and delete permission.
//
// It does NOT refuse loopback, link-local or private addresses, unlike notify: an internal MinIO,
// Ceph or Garage is the normal case, and the caller is an admin. A redirect is a refusal, never a
// hop, and the response body is never echoed — only S3's <Code>, charset-checked.
//
// Ceiling: the probe runs from pstack's networks, not Loki's. An endpoint only Loki can reach fails
// the probe; one only pstack can reach passes it and fails in Loki after the cutover.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// emptySHA256 is hex(SHA-256("")): the probe object is empty.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// probeClient: 10s per request, and a 3xx is answered, not followed (Go rule 12). A var so a test
// can swap the Transport's dialer. http.Client parses Location before asking CheckRedirect, so a 3xx
// with an unparsable Location comes back as `S3 unreachable: …` — still a refusal.
var probeClient = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var s3Code = regexp.MustCompile(`^[A-Za-z]{1,64}$`)

// Probe PUTs then DELETEs an empty pstack-probe-<16 hex> object in s.Bucket. Every failure is a
// *Error (400).
func Probe(ctx context.Context, s S3, secret string) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	key := "pstack-probe-" + hex.EncodeToString(b[:])
	u, err := url.Parse(strings.TrimSuffix(s.Endpoint, "/"))
	if err != nil {
		return &Error{"S3 unreachable: " + err.Error()}
	}
	if s.PathStyle {
		u.Path += "/" + s.Bucket + "/" + key
	} else {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + key
	}
	target := u.String()
	refused, err := probeSend(ctx, http.MethodPut, target, s, secret)
	if err != nil {
		return &Error{"S3 unreachable: " + err.Error()}
	}
	if refused != "" {
		return &Error{"S3 refused the probe: " + refused}
	}
	left := " — " + key + " is left in the bucket"
	refused, err = probeSend(ctx, http.MethodDelete, target, s, secret)
	if err != nil {
		return &Error{"S3 unreachable: " + err.Error() + left}
	}
	if refused != "" {
		return &Error{"S3 refused the probe delete: " + refused + left}
	}
	return nil
}

// probeSend returns "" on a 2xx, else "<status>" or "<status> <Code>". err is a transport error
// with Go's `Put "<url>": ` prefix removed.
func probeSend(ctx context.Context, method, target string, s S3, secret string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return "", err
	}
	sign(req, emptySHA256, s.AccessKeyID, secret, s.Region, now())
	resp, err := probeClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return "", nil
	}
	var e struct{ Code string }
	_ = xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	refused := strconv.Itoa(resp.StatusCode)
	if s3Code.MatchString(e.Code) {
		refused += " " + e.Code
	}
	return refused, nil
}

// sign sets x-amz-date, x-amz-content-sha256 and a SigV4 Authorization for service s3. It signs
// host plus every header already on req. The canonical query is empty: probe requests carry none.
func sign(req *http.Request, payloadHash, keyID, secret, region string, t time.Time) {
	stamp := t.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := []string{"host"}
	values := map[string]string{"host": host}
	for k, vs := range req.Header {
		n := strings.ToLower(k)
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		names = append(names, n)
		values[n] = strings.Join(trimmed, ",")
	}
	sort.Strings(names)
	var headers strings.Builder
	for _, n := range names {
		headers.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	scope := day + "/" + region + "/s3/aws4_request"
	canonical := req.Method + "\n" + req.URL.EscapedPath() + "\n\n" + headers.String() + "\n" + signed + "\n" + payloadHash
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA256([]byte("AWS4"+secret), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	k = hmacSHA256(k, "aws4_request")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+keyID+"/"+scope+
		",SignedHeaders="+signed+",Signature="+hex.EncodeToString(hmacSHA256(k, toSign)))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}
