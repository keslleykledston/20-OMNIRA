// crm-sim is the reference CRM for the OMNIRA headless API (ADR-0048): a small command line program that exercises every path an integration needs, against a
// local or sandbox OMNIRA. It is NOT a product and holds no data; it exists so that an integrator can see each call, header and error once, working.
//
//	OMNIRA_EXTERNAL_URL    https://host/api/v1/external   (no default: it must be named)
//	OMNIRA_EXTERNAL_TOKEN  the integration token (shown once when it is created in Configurações > Integrações > Aplicações externas)
//	SIM_WEBHOOK_SECRET     the signing secret of the webhook endpoint (shown once when it is created)
//
//	crm-sim me                                  who the token is and what it may do
//	crm-sim bootstrap                           checkpoint -> snapshot -> events; prints what it learned
//	crm-sim list [open|closed|all]              conversations the installation sees, with the control version
//	crm-sim show <conversation>                 one conversation and its messages
//	crm-sim claim <conversation>                take a free conversation (no If-Match needed)
//	crm-sim heartbeat <conversation> <version>  renew the lease
//	crm-sim send <conversation> <version> <text>
//	crm-sim transfer <conversation> <version> <queue_id> [user_id]
//	crm-sim release|finalize <conversation> <version>
//	crm-sim listen <addr> <certfile> <keyfile>  receive webhooks: verifies the signature, deduplicates, prints each fact
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/external/crmsim"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: crm-sim <me|bootstrap|list|show|claim|heartbeat|send|transfer|release|finalize|listen> ... (see the header of apps/crm-sim/main.go)")
	os.Exit(2)
}

func must(err error) {
	if err == nil {
		return
	}
	var p *crmsim.Problem
	if errors.As(err, &p) {
		switch {
		case crmsim.IsStale(err):
			fmt.Fprintf(os.Stderr, "412 %s: the conversation changed (now %s). Read it again: crm-sim show <id>\n", p.Code, p.ControlETag)
		case crmsim.IsConflict(err):
			fmt.Fprintf(os.Stderr, "409 %s: %s\n", p.Code, p.Detail)
		default:
			fmt.Fprintf(os.Stderr, "%d %s: %s\n", p.Status, p.Code, p.Detail)
		}
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func version(s string) int64 {
	n, err := strconv.ParseInt(strings.Trim(s, `"`), 10, 64)
	if err != nil || n < 1 {
		fmt.Fprintln(os.Stderr, "the control version must be a positive number")
		os.Exit(2)
	}
	return n
}

func pretty(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "listen" {
		listen(args)
		return
	}
	base, token := os.Getenv("OMNIRA_EXTERNAL_URL"), os.Getenv("OMNIRA_EXTERNAL_TOKEN")
	if base == "" || token == "" {
		fmt.Fprintln(os.Stderr, "set OMNIRA_EXTERNAL_URL and OMNIRA_EXTERNAL_TOKEN")
		os.Exit(2)
	}
	c := crmsim.NewClient(base, token, nil)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	key := crmsim.NewKey("sim") // ONE key per run of one intent: if you run the same command again after a timeout, pass SIM_KEY to repeat it safely
	if k := os.Getenv("SIM_KEY"); k != "" {
		key = k
	}
	need := func(n int) {
		if len(args) < n {
			usage()
		}
	}
	switch cmd {
	case "me":
		var out any
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/me", nil)
		must(err)
		req.Header.Set("Authorization", "Bearer "+token) // the call carries the credential (it did not: /me always answered 401)
		resp, err := c.HTTP.Do(req)
		if err == nil {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			_ = json.Unmarshal(raw, &out)
		}
		must(err)
		pretty(out)
	case "bootstrap":
		s := crmsim.NewSyncer(c)
		must(s.Bootstrap(ctx))
		fmt.Printf("cursor=%d conversations=%d facts_applied=%d\n", s.Cursor, len(s.Control), s.Applied)
	case "list":
		status := "open"
		if len(args) > 0 {
			status = args[0]
		}
		convs, err := c.Conversations(ctx, status)
		must(err)
		for _, v := range convs {
			holder := "-"
			if v.Control.Holder != nil {
				holder = v.Control.Holder.Kind
				if v.Control.Holder.Mine {
					holder += " (me)"
				}
			}
			fmt.Printf("%s  %-6s  control=%-10s v%-4d holder=%s\n", v.ID, v.Status, v.Control.State, v.Control.Version, holder)
		}
	case "show":
		need(1)
		v, err := c.Conversation(ctx, args[0])
		must(err)
		pretty(v)
		msgs, err := c.Messages(ctx, args[0])
		must(err)
		for _, m := range msgs {
			fmt.Printf("%-8s %-9s %s\n", m.Direction, m.Status, m.Body)
		}
	case "claim":
		need(1)
		r, err := c.Claim(ctx, args[0], key, 0)
		must(err)
		pretty(r)
	case "heartbeat":
		need(2)
		r, err := c.Do(ctx, crmsim.Command{Op: "heartbeat", Conv: args[0], Key: key, Version: version(args[1])})
		must(err)
		pretty(r)
	case "send":
		need(3)
		r, err := c.Send(ctx, args[0], key, version(args[1]), strings.Join(args[2:], " "))
		must(err)
		pretty(r) // 202 = QUEUED. Delivery is learned from the message.status_changed events.
	case "transfer":
		need(3)
		user := ""
		if len(args) > 3 {
			user = args[3]
		}
		r, err := c.Transfer(ctx, args[0], key, version(args[1]), args[2], user)
		must(err)
		pretty(r)
	case "release":
		need(2)
		r, err := c.Release(ctx, args[0], key, version(args[1]))
		must(err)
		pretty(r)
	case "finalize":
		need(2)
		r, err := c.Finalize(ctx, args[0], key, version(args[1]), "resolved", "Resolvido pelo CRM de referência")
		must(err)
		pretty(r)
	default:
		usage()
	}
}

func listen(args []string) {
	if len(args) < 3 {
		usage()
	}
	secret := os.Getenv("SIM_WEBHOOK_SECRET")
	if secret == "" {
		fmt.Fprintln(os.Stderr, "set SIM_WEBHOOK_SECRET")
		os.Exit(2)
	}
	rcv := crmsim.NewReceiver([]byte(secret))
	mux := http.NewServeMux()
	mux.Handle("/omnira/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		before := len(rcv.Events())
		rcv.ServeHTTP(w, r)
		if ev := rcv.Events(); len(ev) > before {
			e := ev[len(ev)-1]
			log.Printf("fact seq=%d %s conversation=%s origin=%s mine=%v", e.Seq, e.Type, e.ConversationID, e.Origin.Kind, e.Origin.Mine)
		}
	}))
	srv := &http.Server{Addr: args[0], Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s (POST /omnira/events); configure this URL, https only, in Configurações > Integrações > Aplicações externas", args[0])
	log.Fatal(srv.ListenAndServeTLS(args[1], args[2]))
}
