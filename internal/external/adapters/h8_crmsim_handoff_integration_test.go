package adapters_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/external/crmsim"
)

// The reference CRM is what the integrator's team runs first (apps/crm-sim): this test drives its client over HTTP against the real handlers and real PostgreSQL,
// the way `crm-sim queues`, `poll`, `attach` and `finalize` do, so the hand-over package never ships a command that does not work.
func TestTheReferenceCRMQueuesAttachPollAndFinalizeQuietly(t *testing.T) {
	e := newEnv(t)
	e.mediaAPI()
	tn, _, app, conv, _ := e.writeEnv()
	q := e.queue(tn)
	api := httptest.NewServer(e.ext)
	t.Cleanup(api.Close)
	crm := crmsim.NewClient(api.URL+"/api/v1/external", app.Credential.Token, nil)
	crm.Sleep = func(time.Duration) {}
	ctx := context.Background()

	// the label -> queue map: the company's queues, by id and name
	qs, err := crm.Queues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, x := range qs {
		if x.ID == q.String() && x.Name != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("queues = %+v, want the company's queue %s", qs, q)
	}

	// the loop WITHOUT webhooks: bootstrap, then read the journal; each fact once, even when the same page is read twice
	var facts []crmsim.Event
	sy := crmsim.NewSyncer(crm)
	sy.OnFact = func(ev crmsim.Event) { facts = append(facts, ev) }
	if err := sy.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(facts)
	cl, err := crm.Claim(ctx, conv.String(), "ho-claim-0001", 0)
	if err != nil {
		t.Fatal(err)
	}
	e.sequence() // the worker's sequencer numbers a fact within half a second; here it is called by hand
	if err := sy.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sy.CatchUp(ctx); err != nil { // nothing new: no fact is called again
		t.Fatal(err)
	}
	var sawControl bool
	for _, ev := range facts[before:] {
		if ev.Type == "conversation.control_changed.v1" && ev.ConversationID == conv.String() && ev.Origin.Mine {
			sawControl = true
		}
	}
	if !sawControl {
		t.Fatalf("the claim did not show up in the journal as mine: %+v", facts[before:])
	}
	seen := len(facts)
	if err := sy.CatchUp(ctx); err != nil || len(facts) != seen {
		t.Fatalf("a repeated read called %d new facts (err %v)", len(facts)-seen, err)
	}

	// upload + send, with a caption; the listing then shows the file
	png := tinyPNG(t, 33)
	up, err := crm.UploadAttachment(ctx, conv.String(), "foto.png", png)
	if err != nil {
		t.Fatal(err)
	}
	if up.AttachmentID == "" || up.Kind != "image" || up.Mime != "image/png" {
		t.Fatalf("upload = %+v", up)
	}
	sent, err := crm.SendAttachment(ctx, conv.String(), "ho-send-0001", cl.Control.Version, up.AttachmentID, "Segue a foto")
	if err != nil {
		t.Fatal(err)
	}
	if sent.MessageID == "" || sent.Delivery != "queued" {
		t.Fatalf("send = %+v", sent)
	}
	msgs, err := crm.Messages(ctx, conv.String())
	if err != nil {
		t.Fatal(err)
	}
	var withFile bool
	for _, m := range msgs {
		if m.ID == sent.MessageID && m.Body == "Segue a foto" && m.Type == "image" {
			withFile = true
		}
	}
	if !withFile {
		t.Fatalf("the sent file is not in the listing: %+v", msgs)
	}
	// a file that is not an upload of ours is refused with the code the integrator is told to expect
	if _, err := crm.SendAttachment(ctx, conv.String(), "ho-send-0002", cl.Control.Version, uuid.NewString(), ""); err == nil {
		t.Fatal("a made-up attachment id was accepted")
	}

	// finalize without the protocol notice
	if _, err := crm.FinalizeQuietly(ctx, conv.String(), "ho-final-0001", cl.Control.Version, "resolved", "teste"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM conversations WHERE id=$1 AND status='closed'`, conv); n != 1 {
		t.Fatal("the conversation was not closed")
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound' AND body ILIKE '%protocolo%'`, conv); n != 0 {
		t.Fatalf("finalize with notify_protocol=false still sent a protocol notice (%d)", n)
	}
	_ = http.StatusOK
}
