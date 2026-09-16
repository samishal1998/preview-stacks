package loki

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeFake records every request and answers PUT with put and DELETE with del; a non-2xx carries
// body. verified[i] is whether request i's Authorization re-signs from the request as received.
type probeFake struct {
	put, del int
	body     string

	mu       sync.Mutex
	calls    []string // "<METHOD> <host><path>"
	verified []bool
}

func (f *probeFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	again, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.EscapedPath(), nil)
	at, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	sign(again, r.Header.Get("X-Amz-Content-Sha256"), "AKID", "secret-key", "us-east-1", at)
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.Host+r.URL.Path)
	f.verified = append(f.verified, again.Header.Get("Authorization") == r.Header.Get("Authorization") &&
		r.Header.Get("X-Amz-Content-Sha256") == emptySHA256)
	f.mu.Unlock()
	status := f.put
	if r.Method == http.MethodDelete {
		status = f.del
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if status/100 != 2 {
		_, _ = io.WriteString(w, f.body)
	}
}

func (f *probeFake) snapshot() ([]string, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]bool(nil), f.verified...)
}

var probeKeyRe = regexp.MustCompile(`^pstack-probe-[0-9a-f]{16}$`)

// probePutThenDelete asserts exactly PUT then DELETE of one pstack-probe-<16 hex> key under prefix
// (`<host><path up to the key>`), both signed, and returns the key.
func probePutThenDelete(t *testing.T, f *probeFake, prefix string) string {
	t.Helper()
	calls, verified := f.snapshot()
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want PUT then DELETE", calls)
	}
	key := strings.TrimPrefix(calls[0], "PUT "+prefix)
	if !probeKeyRe.MatchString(key) || calls[1] != "DELETE "+prefix+key {
		t.Fatalf("calls = %q, want PUT then DELETE of %spstack-probe-<16 hex>", calls, prefix)
	}
	if !verified[0] || !verified[1] {
		t.Fatalf("signatures verified = %v, want both", verified)
	}
	return key
}

func probeS3(endpoint string, pathStyle bool) S3 {
	return S3{Endpoint: endpoint, Region: "us-east-1", Bucket: "logs", PathStyle: pathStyle, AccessKeyID: "AKID"}
}

// negative control: sign only host and x-amz-* (skip the other headers on req) → range is not
// signed and the Authorization differs from AWS's published one.
func TestSignMatchesAWSGetObjectExample(t *testing.T) {
	// AWS's S3 SigV4 example "GET Object" (sig-v4-header-based-auth): examplebucket, test.txt,
	// Range bytes=0-9, 2013-05-24, the documented example key pair.
	req, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-9")
	sign(req, emptySHA256, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization\n got %s\nwant %s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20130524T000000Z" {
		t.Fatalf("x-amz-date = %q", req.Header.Get("X-Amz-Date"))
	}
}

// negative control: build the path-style URL without the bucket → the server sees
// /pstack-probe-… with no /logs/ segment.
func TestProbePathStyle(t *testing.T) {
	f := &probeFake{put: 200, del: 204}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	if err := Probe(context.Background(), probeS3(srv.URL+"/", true), "secret-key"); err != nil {
		t.Fatal(err)
	}
	probePutThenDelete(t, f, strings.TrimPrefix(srv.URL, "http://")+"/logs/")
}

// negative control: ignore PathStyle (always path-style) → the server sees host minio.test:9000
// and /logs/pstack-probe-….
func TestProbeVirtualHosted(t *testing.T) {
	f := &probeFake{put: 200, del: 200}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	c := *probeClient
	c.Transport = tr
	old := probeClient
	probeClient = &c
	t.Cleanup(func() { probeClient = old })

	if err := Probe(context.Background(), probeS3("http://minio.test:9000", false), "secret-key"); err != nil {
		t.Fatal(err)
	}
	probePutThenDelete(t, f, "logs.minio.test:9000/")
}

// negative control: append the response body to the refusal (read it, then unmarshal) → the
// message carries "does not exist in our records".
func TestProbeRefusedPutNeverEchoesTheBody(t *testing.T) {
	f := &probeFake{put: 403, body: `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Error><Code>InvalidAccessKeyId</Code><Message>The AWS Access Key Id you provided does not exist in our records.</Message><RequestId>4442587FB7D0A2F9</RequestId></Error>`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	err := Probe(context.Background(), probeS3(srv.URL, true), "secret-key")
	if !IsError(err) || err.Error() != "S3 refused the probe: 403 InvalidAccessKeyId" {
		t.Fatalf("err = %v, want *Error `S3 refused the probe: 403 InvalidAccessKeyId`", err)
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("the body leaked: %v", err)
	}
	if calls, _ := f.snapshot(); len(calls) != 1 {
		t.Fatalf("calls = %q, want the PUT alone (no DELETE after a refused PUT)", calls)
	}
}

// negative control: drop the ^[A-Za-z]{1,64}$ check (append any non-empty Code) → the message
// carries "<script>".
func TestProbeRefusedPutOmitsACodeOutsideTheCharset(t *testing.T) {
	f := &probeFake{put: 400, body: `<Error><Code>&lt;script&gt;</Code></Error>`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	err := Probe(context.Background(), probeS3(srv.URL, true), "secret-key")
	if !IsError(err) || err.Error() != "S3 refused the probe: 400" {
		t.Fatalf("err = %v, want *Error `S3 refused the probe: 400`", err)
	}
}

// negative control: delete CheckRedirect from probeClient → the client follows the 302 to target,
// which records a request, and the probe passes.
func TestProbeDoesNotFollowRedirects(t *testing.T) {
	target := &probeFake{put: 200, del: 200}
	tsrv := httptest.NewServer(target)
	t.Cleanup(tsrv.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tsrv.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirect.Close)
	err := Probe(context.Background(), probeS3(redirect.URL, true), "secret-key")
	if !IsError(err) || err.Error() != "S3 refused the probe: 302" {
		t.Fatalf("err = %v, want `S3 refused the probe: 302`", err)
	}
	if calls, _ := target.snapshot(); len(calls) != 0 {
		t.Fatalf("the redirect was followed: %q", calls)
	}
}

// negative control: drop the key from the delete refusal → the message no longer names the object.
func TestProbeRefusedDeleteNamesTheObject(t *testing.T) {
	f := &probeFake{put: 200, del: 403, body: `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	err := Probe(context.Background(), probeS3(srv.URL, true), "secret-key")
	key := probePutThenDelete(t, f, strings.TrimPrefix(srv.URL, "http://")+"/logs/")
	want := "S3 refused the probe delete: 403 AccessDenied — " + key + " is left in the bucket"
	if !IsError(err) || err.Error() != want {
		t.Fatalf("err = %v, want *Error %q", err, want)
	}
}

// negative control: return the client error without unwrapping *url.Error → the message carries
// `Put "http://…"`.
func TestProbeUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL
	srv.Close()
	err := Probe(context.Background(), probeS3(endpoint, true), "secret-key")
	if !IsError(err) || !strings.HasPrefix(err.Error(), "S3 unreachable: ") || strings.Contains(err.Error(), `Put "`) {
		t.Fatalf("err = %v, want *Error `S3 unreachable: <dial error>`", err)
	}
}
