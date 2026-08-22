package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/events"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/webhooks"
)

// negative control: replace the netip branch with a `strings.HasPrefix(host, "fc")` test → fcm
// is refused and [fc00::1] (bracketed) passes.
func TestIsPrivateHost(t *testing.T) {
	private := []string{"127.0.0.1", "0.0.0.0", "10.1.2.3", "192.168.1.5", "172.16.0.9", "172.31.255.255", "169.254.169.254",
		"::1", "::", "fc00::1", "fd12:3456::1", "fe80::1", "fe80::1%eth0", "::ffff:127.0.0.1", "::ffff:7f00:1", "[::1]", "[fc00::1]",
		"localhost", "LOCALHOST.", "api.localhost"}
	public := []string{"172.32.0.1", "172.15.0.1", "8.8.8.8", "fcm.googleapis.com", "fd-cdn.example.com", "fe80-notreally.com",
		"localhost.mycompany.com", "2606:4700::1111", "ff02::1", "example.com."}
	for _, h := range private {
		if !IsPrivateHost(h) {
			t.Errorf("%s should be private", h)
		}
	}
	for _, h := range public {
		if IsPrivateHost(h) {
			t.Errorf("%s should be public", h)
		}
	}
}

// negative control: drop the scheme check → "ftp://x/" is accepted.
func TestAssertDeliverableURL(t *testing.T) {
	t.Setenv("PSTACK_NOTIFY_ALLOW_PRIVATE", "")
	for raw, want := range map[string]string{
		"not a url":                      `"not a url" is not a URL`,
		"http://":                        `"http://" is not a URL`,
		"ftp://x.example/":               `only http and https are deliverable, got "ftp:"`,
		"http://[::ffff:127.0.0.1]/hook": `"::ffff:127.0.0.1" is a loopback, link-local or private address. The control plane would be making the request, so this can reach services only it can see — including cloud metadata. Set PSTACK_NOTIFY_ALLOW_PRIVATE=1 if that is genuinely what you want.`,
	} {
		_, err := AssertDeliverableURL(raw)
		if !IsError(err) || err.Error() != want {
			t.Errorf("%s: got %v", raw, err)
		}
	}
	if _, err := AssertDeliverableURL("https://fcm.googleapis.com/hook"); err != nil {
		t.Error(err)
	}
	t.Setenv("PSTACK_NOTIFY_ALLOW_PRIVATE", "1")
	if _, err := AssertDeliverableURL("http://127.0.0.1:1/hook"); err != nil {
		t.Error(err)
	}
}

// negative control: sign the body alone (without "<at>.") → the receiver's check fails.
func TestEnvelopeAndSign(t *testing.T) {
	e := events.Event{ID: "evt_1", Event: "job.failed", At: 1700000000000, Data: json.RawMessage(`{"stack":"pr-1","x":"<b>&"}`), Replay: true}
	body := Envelope(e)
	if string(body) != `{"id":"evt_1","event":"job.failed","at":1700000000000,"data":{"stack":"pr-1","x":"<b>&"}}` {
		t.Fatalf("envelope %s", body)
	}
	m := hmac.New(sha256.New, []byte("whsec_x"))
	m.Write([]byte("1700000000000." + string(body)))
	if Sign("whsec_x", e.At, body) != "sha256="+hex.EncodeToString(m.Sum(nil)) {
		t.Fatal("signature")
	}
}

// negative control: change "Teardown LEAKED" → the job.leaked line differs.
func TestSummarize(t *testing.T) {
	ev := func(name, data string) events.Event { return events.Event{Event: name, Data: json.RawMessage(data)} }
	cases := map[string]string{
		"Test delivery from pstack — the connection works. No job ran.":                              Summarize(ev("job.succeeded", `{"test":true}`)),
		"Deploy succeeded on pr-1 in 2s.":                                                            Summarize(ev("job.succeeded", `{"stack":"pr-1","action":"up","durationMs":1600}`)),
		"Deploy succeeded on pr-1 in 1s.":                                                            Summarize(ev("job.succeeded", `{"stack":"pr-1","action":"up","durationMs":20}`)),
		"Teardown on pr-1 was CANCELLED by alice in 3s. Nothing was undone — verify what exists.":    Summarize(ev("job.cancelled", `{"stack":"pr-1","action":"down","cancelledBy":"alice","durationMs":3000}`)),
		"Deploy FAILED on pr-1. boom":                                                                Summarize(ev("job.failed", `{"stack":"pr-1","action":"up","error":"boom"}`)),
		"Teardown LEAKED on pr-1 — resources survived and nothing will retry. Leaked: db, bucket.":   Summarize(ev("job.leaked", `{"stack":"pr-1","action":"down","leakedAxes":["db","bucket"]}`)),
		"Deployment 7 created (stack 7).":                                                            Summarize(ev("deployment.created", `{"id":7}`)),
		"Spec web replaced.":                                                                         Summarize(ev("spec.stored", `{"name":"web","replaced":true}`)),
		"web-1 healthcheck never settled on pr-1 — still starting after 31s.":                        Summarize(ev("healthcheck.timedout", `{"container":"web-1","stack":"pr-1","waitedMs":30500}`)),
		"web-1 is ready on pr-1 (running; no healthcheck to check).":                                 Summarize(ev("container.ready", `{"container":"web-1","stack":"pr-1"}`)),
		"pr-1 is ready — 2/2 container(s) up in 5s.":                                                 Summarize(ev("stack.ready", `{"stack":"pr-1","ready":2,"containers":2,"durationMs":5000}`)),
		"pr-1 did not come up. Failed: db.":                                                          Summarize(ev("stack.failed", `{"stack":"pr-1","failedContainers":["db"]}`)),
		"pr-1 went to sleep (idle 30m). Its data and axes stay; a request to its hostname wakes it.": Summarize(ev("stack.slept", `{"stack":"pr-1","reason":"idle 30m"}`)),
		"A read-only link to pr-1 was created by bob (logs, status).":                                Summarize(ev("share.created", `{"stack":"pr-1","by":"bob","views":["logs","status"]}`)),
		"custom.event on this host.":                                                                 Summarize(ev("custom.event", `{}`)),
		"Wake started on sleepy.":                                                                    Summarize(ev("job.started", `{"stack":"sleepy","action":"wake"}`)),
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("\n got %q\nwant %q", got, want)
		}
	}
}

// negative control: drop the `secret[k]` test in PublicConfig → the Slack URL is returned verbatim.
func TestPublicConfigAndTypes(t *testing.T) {
	cfg := omap.From("webhookUrl", "https://hooks.slack.com/services/T/B/xxxxxxxx", "channel", "#ops")
	pub := PublicConfig("slack", cfg)
	if pub.GetString("webhookUrl") == cfg.GetString("webhookUrl") || pub.GetString("channel") != "#ops" || pub.Keys()[0] != "webhookUrl" {
		t.Fatalf("public %v", pub)
	}
	if PublicConfig("webhook", cfg) != cfg || PublicConfig("nope", cfg) != cfg {
		t.Fatal("non-secret types return the same map")
	}
	_, err := TypeOf("constructor")
	if !IsError(err) || err.Error() != `unknown notifier type "constructor" — known types: webhook, slack, discord` {
		t.Fatalf("typeOf: %v", err)
	}
	if err := ValidateConfig("webhook", omap.New()); !IsError(err) || err.Error() != "a webhook needs a `url`" {
		t.Fatalf("validate: %v", err)
	}
	if err := ValidateConfig("discord", omap.From("webhookUrl", " ")); !IsError(err) || err.Error() != "a Discord notifier needs a `webhookUrl`" {
		t.Fatalf("validate discord: %v", err)
	}
	if got := RedactForNotifier("failed to reach https://hooks.slack.com/services/T/B/xxxxxxxx (#ops)", "whsec_abcdefgh", cfg); strings.Contains(got, "xxxxxxxx") || !strings.Contains(got, "#ops") {
		t.Fatalf("redact %q", got)
	}
}

type receiver struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
	code int
	loc  string
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.body = append(r.body, string(b))
	r.mu.Unlock()
	if r.loc != "" {
		w.Header().Set("Location", r.loc)
	}
	w.WriteHeader(r.code)
}

func (r *receiver) count() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.reqs) }

func setup(t *testing.T, code, status int) (*Dispatcher, *webhooks.Webhooks, *receiver, *httptest.Server) {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	rc := &receiver{code: code}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)
	hooks := webhooks.New(s, PublicConfig)
	d := New(hooks)
	d.RetryDelays = []time.Duration{10 * time.Millisecond, 10 * time.Millisecond}
	d.AttemptTimeout = 2 * time.Second
	_ = status
	return d, hooks, rc, srv
}

func waitDelivered(t *testing.T, hooks *webhooks.Webhooks, notifierID int64, n int) []webhooks.DeliveryRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := hooks.Deliveries(notifierID, 500)
		done := 0
		for _, r := range rows {
			if r.Status != "pending" {
				done++
			}
		}
		if done >= n {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deliveries for %d never settled", notifierID)
	return nil
}

// negative control: drop the `x-pstack-signature` header → the receiver-side HMAC check fails.
func TestDispatchSignedDeliveryAndRedelivery(t *testing.T) {
	d, hooks, rc, srv := setup(t, 200, 200)
	row, secret, err := hooks.Create(webhooks.CreateArgs{Type: "webhook", Name: "n", Config: omap.From("url", srv.URL+"/hook"), Events: []string{"job.failed"}, Signs: true})
	if err != nil {
		t.Fatal(err)
	}
	e := events.Event{ID: "evt_1", Event: "job.failed", At: 1700000000000, Data: json.RawMessage(`{"stack":"pr-1","action":"up"}`)}
	d.Dispatch(events.Event{ID: "evt_0", Event: "job.started", At: 1, Data: json.RawMessage(`{}`)}) // not subscribed
	d.Dispatch(e)
	rows := waitDelivered(t, hooks, row.ID, 1)
	if len(rows) != 1 || rows[0].Status != "ok" || rows[0].Attempts != 1 || *rows[0].ResponseCode != 200 || rows[0].Error != nil {
		t.Fatalf("rows %+v", rows)
	}
	req := rc.reqs[0]
	body := rc.body[0]
	if body != `{"id":"evt_1","event":"job.failed","at":1700000000000,"data":{"stack":"pr-1","action":"up"}}` {
		t.Fatalf("body %s", body)
	}
	if req.Header.Get("x-pstack-event") != "job.failed" || req.Header.Get("x-pstack-delivery") != "evt_1" || req.Header.Get("x-pstack-timestamp") != "1700000000000" ||
		req.Header.Get("user-agent") != "pstack" || req.Header.Get("content-type") != "application/json" || req.Header.Get("x-pstack-redelivery") != "" {
		t.Fatalf("headers %v", req.Header)
	}
	m := hmac.New(sha256.New, []byte(*secret))
	m.Write([]byte("1700000000000." + body))
	if req.Header.Get("x-pstack-signature") != "sha256="+hex.EncodeToString(m.Sum(nil)) {
		t.Fatal("signature does not verify")
	}
	dl, _ := hooks.DeliveryWithPayload(rows[0].ID)
	if *dl.Payload != body {
		t.Fatal("stored payload is not the sent bytes")
	}
	got, _ := hooks.Get(row.ID)
	if got.LastStatus == nil || *got.LastStatus != "ok" {
		t.Fatal("noteResult")
	}
	// redelivery: same id, marked
	sub, _ := hooks.SubscribedTo("job.failed")
	d.Redeliver(sub[0], e)
	waitDelivered(t, hooks, row.ID, 2)
	if rc.count() != 2 || rc.reqs[1].Header.Get("x-pstack-redelivery") != "1" || rc.reqs[1].Header.Get("x-pstack-delivery") != "evt_1" || rc.body[1] != body {
		t.Fatalf("redelivery %v", rc.reqs[1].Header)
	}
}

// negative control: remove CheckRedirect from the client → the 302 is followed and the delivery is ok.
func TestRedirectIsFailureAndRetries(t *testing.T) {
	d, hooks, rc, srv := setup(t, 302, 0)
	rc.loc = "http://169.254.169.254/latest/"
	row, _, _ := hooks.Create(webhooks.CreateArgs{Type: "webhook", Name: "n", Config: omap.From("url", srv.URL+"/hook"), Events: []string{"*"}, Signs: true})
	d.Dispatch(events.Event{ID: "evt_2", Event: "job.failed", At: 2, Data: json.RawMessage(`{}`)})
	rows := waitDelivered(t, hooks, row.ID, 1)
	if rows[0].Status != "failed" || rows[0].Attempts != 3 || *rows[0].ResponseCode != 302 || *rows[0].Error != "redirect to http://169.254.169.254/latest/ not followed" {
		t.Fatalf("rows %+v err=%v", rows[0], *rows[0].Error)
	}
	if rc.count() != 3 {
		t.Fatalf("%d attempts", rc.count())
	}
}

// negative control: delete the `busy` check in pump → both events fly at once and the receiver can
// see evt_b before evt_a.
func TestPerNotifierOrderingAndDeletedBeforeRetry(t *testing.T) {
	d, hooks, rc, srv := setup(t, 500, 0)
	d.RetryDelays = []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}
	row, _, _ := hooks.Create(webhooks.CreateArgs{Type: "slack", Name: "s", Config: omap.From("webhookUrl", srv.URL+"/slack"), Events: []string{"*"}})
	d.Dispatch(events.Event{ID: "evt_a", Event: "job.failed", At: 1, Data: json.RawMessage(`{"stack":"pr-1","action":"up"}`)})
	d.Dispatch(events.Event{ID: "evt_b", Event: "job.failed", At: 2, Data: json.RawMessage(`{"stack":"pr-2","action":"up"}`)})
	if d.Queued(row.ID) != 1 {
		t.Fatalf("queued %d", d.Queued(row.ID))
	}
	// evt_a is mid-retry; delete the notifier → "notifier deleted before retry" and evt_b can never
	// open a delivery row (FK) — the dispatcher must survive that.
	time.Sleep(20 * time.Millisecond)
	if ok, _ := hooks.Remove(row.ID); !ok {
		t.Fatal("remove")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && d.Queued(row.ID) > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	n := rc.count()
	if n < 1 || n > 2 {
		t.Fatalf("%d requests (expected 1 or 2: attempt 1, maybe attempt 2, never evt_b)", n)
	}
	for _, b := range rc.body {
		if b != `{"text":"Deploy FAILED on pr-1."}` {
			t.Fatalf("body %s", b)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inFlight != 0 || len(d.busy) != 0 {
		t.Fatalf("dispatcher state leaked: inFlight=%d busy=%v", d.inFlight, d.busy)
	}
}

// negative control: remove the MaxQueuedPerNotifier shift → 210 rows are queued and none dropped.
func TestQueueOverflowDropsOldest(t *testing.T) {
	d, hooks, _, srv := setup(t, 200, 0)
	row, _, _ := hooks.Create(webhooks.CreateArgs{Type: "webhook", Name: "n", Config: omap.From("url", srv.URL+"/hook"), Events: []string{"*"}, Signs: true})
	sub, _ := hooks.SubscribedTo("job.failed")
	// fill the queue directly while nothing pumps, then look at the drop rows
	for i := 0; i < MaxQueuedPerNotifier+3; i++ {
		d.enqueue(sub[0], events.Event{ID: "evt_" + strings.Repeat("x", i%5), Event: "job.failed", At: int64(i), Data: json.RawMessage(`{}`)}, false)
	}
	if d.Queued(row.ID) != MaxQueuedPerNotifier {
		t.Fatalf("queued %d", d.Queued(row.ID))
	}
	rows, _ := hooks.Deliveries(row.ID, 500)
	if len(rows) != 3 {
		t.Fatalf("%d drop rows", len(rows))
	}
	for _, r := range rows {
		if r.Status != "failed" || r.Attempts != 0 || *r.Error != "dropped — 200 events already waiting for this notifier, and it is not keeping up" {
			t.Fatalf("drop row %+v", r)
		}
		if dl, _ := hooks.DeliveryWithPayload(r.ID); dl.Payload != nil {
			t.Fatal("a drop has no envelope")
		}
	}
}
