package webhooks

import (
	"strings"
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/jsonx"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/omap"
	"github.com/samishal1998/preview-stacks/packages/pstack/internal/store"
)

func open(t *testing.T, pub PublicConfig) *Webhooks {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s, pub)
}

// negative control: add `secret` to publicColumns → the marshalled row contains "whsec_".
func TestCreateListGetNoSecretLeak(t *testing.T) {
	w := open(t, func(kind string, c *omap.Map) *omap.Map {
		m := c.Clone()
		if m.Has("token") {
			m.Set("token", "••••")
		}
		return m
	})
	cfg := omap.From("url", "https://x.example/hook", "token", "t0p")
	row, secret, err := w.Create(CreateArgs{Type: "webhook", Name: "ci bot", Config: cfg, Events: []string{"job.failed", "job.failed", "*"}, Signs: true})
	if err != nil {
		t.Fatal(err)
	}
	if secret == nil || !strings.HasPrefix(*secret, "whsec_") || len(*secret) != 6+48 {
		t.Fatalf("secret %v", secret)
	}
	b, _ := jsonx.Marshal(row)
	if strings.Contains(string(b), "whsec_") || !strings.Contains(string(b), `"events":["job.failed","*"]`) || !strings.Contains(string(b), `"lastStatus":null,"lastAt":null`) {
		t.Fatalf("row %s", b)
	}
	// Create returns the UNMASKED config (like the TS); List/Get mask.
	if row.Config.GetString("token") != "t0p" {
		t.Fatalf("create config %v", row.Config)
	}
	list, _ := w.List()
	if len(list) != 1 || list[0].Config.GetString("token") != "••••" || list[0].Config.GetString("url") != "https://x.example/hook" {
		t.Fatalf("list %+v", list)
	}
	got, _ := w.Get(row.ID)
	if got == nil || got.Config.GetString("token") != "••••" {
		t.Fatalf("get %+v", got)
	}
	raw, _ := w.RawConfigOf(row.ID)
	if raw.GetString("token") != "t0p" {
		t.Fatalf("raw %v", raw)
	}
	s, _ := w.SecretOf(row.ID)
	if s == nil || *s != *secret {
		t.Fatal("SecretOf")
	}
	if s, _ := w.SecretOf(999); s != nil {
		t.Fatal("SecretOf unknown must be nil")
	}
	if g, _ := w.Get(999); g != nil {
		t.Fatal("Get unknown must be nil")
	}
	subs, _ := w.SubscribedTo("stack.woken")
	if len(subs) != 1 || subs[0].Config.GetString("token") != "t0p" {
		t.Fatalf("wildcard subscription unmasked: %+v", subs)
	}
	if ok, _ := w.SetEnabled(row.ID, false); !ok {
		t.Fatal("disable")
	}
	if subs, _ := w.SubscribedTo("job.failed"); len(subs) != 0 {
		t.Fatal("disabled notifier still subscribed")
	}
	// non-signing type: empty secret column, nil returned
	_, secret2, err := w.Create(CreateArgs{Type: "slack", Name: "s", Config: omap.New(), Events: []string{"job.failed"}, Signs: false})
	if err != nil || secret2 != nil {
		t.Fatalf("non-signing: %v %v", secret2, err)
	}
	if ok, _ := w.Remove(row.ID); !ok {
		t.Fatal("remove")
	}
	if ok, _ := w.Remove(row.ID); ok {
		t.Fatal("remove twice")
	}
}

// negative control: change the unknown-event sentence → exact-message assertions fail.
func TestCreateRefusals(t *testing.T) {
	w := open(t, nil)
	_, _, err := w.Create(CreateArgs{Type: "webhook", Name: "bad\nname", Events: []string{"*"}})
	if !IsError(err) || !strings.HasPrefix(err.Error(), "name must be 1–64 characters of letters, digits, space, or . : @ / _ -  (got \"bad\nname\")") {
		t.Fatalf("name: %v", err)
	}
	_, _, err = w.Create(CreateArgs{Type: "webhook", Name: "ok", Events: []string{}})
	if !IsError(err) || !strings.HasPrefix(err.Error(), "subscribe to at least one event. Known events: deployment.created, ") {
		t.Fatalf("empty: %v", err)
	}
	_, _, err = w.Create(CreateArgs{Type: "webhook", Name: "ok", Events: []string{"job.failed", "nope", "x"}})
	if !IsError(err) || !strings.HasPrefix(err.Error(), "unknown event(s): nope, x. Known events: deployment.created, ") || !strings.HasSuffix(err.Error(), ` (or "*" for all)`) {
		t.Fatalf("unknown: %v", err)
	}
	_, _, err = w.Create(CreateArgs{Type: "webhook", Name: "ok", Events: []string{"*"}, ValidateConfig: func(string, *omap.Map) error { return &Error{"url is required"} }})
	if !IsError(err) || err.Error() != "url is required" {
		t.Fatalf("validate: %v", err)
	}
}

// negative control: drop `status != 'pending'` from Prune → the pending row is deleted.
func TestDeliveriesAndPrune(t *testing.T) {
	w := open(t, nil)
	row, _, _ := w.Create(CreateArgs{Type: "webhook", Name: "n", Events: []string{"*"}, Signs: true})
	id, err := w.StartDelivery(row.ID, "evt_1", "job.failed", []byte(`{"id":"evt_1","event":"job.failed","at":1,"data":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := w.DeliveryWithPayload(id)
	if d == nil || d.Payload == nil || *d.Payload != `{"id":"evt_1","event":"job.failed","at":1,"data":{}}` {
		t.Fatalf("payload %+v", d)
	}
	nilID, _ := w.StartDelivery(row.ID, "evt_2", "job.failed", nil)
	if d, _ := w.DeliveryWithPayload(nilID); d.Payload != nil {
		t.Fatal("nil payload must stay NULL")
	}
	if d, _ := w.DeliveryWithPayload(999); d != nil {
		t.Fatal("unknown delivery must be nil")
	}
	code := int64(502)
	if err := w.FinishDelivery(id, Result{Status: "failed", Attempts: 3, ResponseCode: &code, Error: "<html>\n<body>" + strings.Repeat("x", 400)}); err != nil {
		t.Fatal(err)
	}
	_ = w.NoteResult(row.ID, "failed")
	list, _ := w.Deliveries(row.ID, 0)
	if len(list) != 1 {
		t.Fatalf("limit clamp to 1: %d", len(list))
	}
	list, _ = w.Deliveries(row.ID, 50)
	b, _ := jsonx.Marshal(list)
	if !strings.Contains(string(b), `"status":"failed","attempts":3,"responseCode":502,"error":"<html>"`) || !strings.Contains(string(b), `"responseCode":null,"error":null`) {
		t.Fatalf("deliveries %s", b)
	}
	got, _ := w.Get(row.ID)
	if got.LastStatus == nil || *got.LastStatus != "failed" || got.LastAt == nil {
		t.Fatalf("noteResult %+v", got)
	}
	// fill past the cap; the pending row must survive
	for i := 0; i < MaxDeliveriesPerNotifier+5; i++ {
		did, _ := w.StartDelivery(row.ID, "evt_fill", "job.failed", nil)
		_ = w.FinishDelivery(did, Result{Status: "ok", Attempts: 1})
	}
	if err := w.Prune(row.ID); err != nil {
		t.Fatal(err)
	}
	all, _ := w.Deliveries(row.ID, 500)
	if len(all) > MaxDeliveriesPerNotifier+1 {
		t.Fatalf("%d rows after prune", len(all))
	}
	if d, _ := w.DeliveryWithPayload(nilID); d == nil {
		t.Fatal("pending delivery pruned")
	}
	if d, _ := w.DeliveryWithPayload(id); d != nil {
		t.Fatal("oldest finished delivery should have been pruned")
	}
	_, _ = w.Remove(row.ID)
	if all, _ := w.Deliveries(row.ID, 500); len(all) != 0 {
		t.Fatal("cascade")
	}
	if all, _ := w.Deliveries(row.ID, 500); all == nil {
		t.Fatal("empty must be [] not null")
	}
}
