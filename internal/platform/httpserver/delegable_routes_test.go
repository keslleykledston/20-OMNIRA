package httpserver

// The allow-list of routes a Hub agent may reach in the delegated context (ADR-0040) is the default-deny gate of the whole feature: a route that is
// not on it is a 404 for a delegate, and each one names the permission key it needs. It lives in this file's neighbour (server.go) as the
// `tenancyadapters.Delegable("<key>", ...)` wrapper of each route, so the list is read from the source and compared with the list below. Adding a
// route to the delegated context, or changing its key, must be a visible change HERE too (and have its own tests where it is built).

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestTheDelegableRoutesAndTheirKeysAreExactlyTheReviewedOnes(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`s\.mux\.Handle\("([A-Z]+ [^"]+)",\s*tenancyadapters\.Delegable\("([^"]+)"`)
	got := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		if prev, dup := got[m[1]]; dup && prev != m[2] {
			t.Errorf("%s is registered with two different keys: %s and %s", m[1], prev, m[2])
		}
		got[m[1]] = m[2]
	}
	// DelegableAny: the keys follow the wrapped handler, one route per line ("a|b" = either key opens it)
	reAny := regexp.MustCompile(`(?m)s\.mux\.Handle\("([A-Z]+ [^"]+)",\s*tenancyadapters\.DelegableAny\(.*?\)+, ((?:"[^"]+"(?:, )?)+)\)\)$`)
	for _, m := range reAny.FindAllStringSubmatch(string(src), -1) {
		keys := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[2], -1)
		var names []string
		for _, k := range keys {
			names = append(names, k[1])
		}
		sort.Strings(names)
		got[m[1]] = strings.Join(names, "|")
	}
	// the delegable routes declared outside server.go: the attendance module's (finalize, whose decorator is installed by RegisterAttendanceHandlers, and the
	// history/follow-up routes of phase 06, registered with `base` = the tenant prefix)
	if att, err := os.ReadFile("../../attendance/adapters/http.go"); err == nil {
		if m := regexp.MustCompile(`tenancyadapters\.Delegable\("([^"]+)", wrap\(h\.delegatedFinalize\(`).FindStringSubmatch(string(att)); m != nil {
			got["POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/finalize"] = m[1]
		}
		// phase 07: the AI closing draft, made delegable only when the server wires the conversation.read check (otherwise the route stays closed)
		if m := regexp.MustCompile(`suggestRoute = tenancyadapters\.Delegable\("([^"]+)", wrap\(h\.suggest\)\)`).FindStringSubmatch(string(att)); m != nil {
			got["POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/finalize/suggest"] = m[1]
		}
		if m := regexp.MustCompile(`searchRoute = tenancyadapters\.Delegable\("([^"]+)", wrap\(h\.searchHistory\)\)`).FindStringSubmatch(string(att)); m != nil {
			got["GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/history-search"] = m[1]
		}
		for _, m := range regexp.MustCompile(`mux\.Handle\("([A-Z]+) "\+base\+"([^"]+)", tenancyadapters\.Delegable\("([^"]+)"`).FindAllStringSubmatch(string(att), -1) {
			got[m[1]+" /api/v1/tenants/{tenant_id}"+m[2]] = m[3]
		}
	} else {
		t.Fatal(err)
	}
	const tn = "/api/v1/tenants/{tenant_id}"
	want := map[string]string{
		// phase 03: attend the conversation
		"GET " + tn + "/me/access":                             "conversation.read",
		"GET " + tn + "/inbox/conversations":                   "conversation.read",
		"GET " + tn + "/inbox/conversations/{conversation_id}": "conversation.read",
		// ADR-0044: where a conversation is routed and why; reviewed (names and states only; same visibility as the conversation read; tested in hub/adapters)
		"GET " + tn + "/inbox/conversations/{conversation_id}/routing":            "conversation.read",
		"GET " + tn + "/inbox/conversations/{conversation_id}/messages":           "conversation.read",
		"GET " + tn + "/inbox/conversations/{conversation_id}/timeline":           "conversation.read",
		"GET " + tn + "/messages/{message_id}/media":                              "media.read",
		"GET /api/v1/hubs/{hub_id}/serve/{tenant_id}/messages/{message_id}/media": "media.read",
		"GET " + tn + "/contacts/{contact_id}":                                    "contact.read",
		"POST " + tn + "/inbox/conversations/{conversation_id}/assign":            "conversation.claim",
		"POST " + tn + "/inbox/conversations/{conversation_id}/messages":          "conversation.reply",
		// phase 04a: classify and edit the contact
		"PATCH " + tn + "/contacts/{contact_id}":                           "contact.classify",
		"PUT " + tn + "/contacts/{contact_id}/details":                     "contact.classify",
		"GET " + tn + "/contacts/{contact_id}/classification":              "account.read",
		"PUT " + tn + "/contacts/{contact_id}/classification":              "contact.classify",
		"POST " + tn + "/contacts/{contact_id}/accounts":                   "contact.classify",
		"POST " + tn + "/contacts/{contact_id}/accounts/{link_id}/end":     "contact.classify",
		"POST " + tn + "/contacts/{contact_id}/accounts/{link_id}/primary": "contact.classify",
		"GET " + tn + "/accounts":                                          "account.read",
		// phase 04b: the instance's ERP directory and the ERP ticket of a conversation the agent holds
		"GET " + tn + "/crm/companies":                             "contact.classify|ticket.create",
		"GET " + tn + "/contacts/{contact_id}/company-suggestions": "account.read",
		"GET " + tn + "/conversations/{conversation_id}/ticket":    "ticket.create|ticket.read",
		"POST " + tn + "/conversations/{conversation_id}/ticket":   "ticket.create",
		// files for a Hub agent (ADR-0024 x ADR-0040): upload and remove need the reply key; the wrapper sends a delegated request to the Hub's write path, never to the member's handler
		"POST " + tn + "/inbox/conversations/{conversation_id}/attachments":                   "conversation.reply",
		"DELETE " + tn + "/inbox/conversations/{conversation_id}/attachments/{attachment_id}": "conversation.reply",
		// what the composer may offer on the line (a delegate cannot read channel_connections: the Hub's write path answers)
		"GET " + tn + "/inbox/conversations/{conversation_id}/channel": "conversation.read",
		// refreshing the ERP projection of the ticket: the same key as opening it (the service asks the database for the person of the context). Changing the ERP status is NOT here on purpose.
		"POST " + tn + "/conversations/{conversation_id}/ticket/refresh": "ticket.create",
		"POST " + tn + "/conversations/{conversation_id}/ticket/status":  "ticket.update", // the company's switch hub_ticket_status (on by default) decides on top of the key
		// phase 05: the realtime streams (admitted in a short transaction, rechecked every 15 s)
		"GET " + tn + "/inbox/events":                                    "conversation.read",
		"GET " + tn + "/inbox/conversations/{conversation_id}/events":    "conversation.read",
		"POST " + tn + "/conversations/{conversation_id}/ai/summary":     "ai.assist",
		"POST " + tn + "/inbox/conversations/{conversation_id}/finalize": "conversation.finalize",
		// phase 04c: hand over / give back a conversation the agent holds, and the notes of a contact
		"POST " + tn + "/inbox/conversations/{conversation_id}/unassign":           "conversation.claim",
		"POST " + tn + "/inbox/conversations/{conversation_id}/transfer":           "conversation.transfer",
		"GET " + tn + "/inbox/conversations/{conversation_id}/transfer-candidates": "conversation.transfer",
		"GET " + tn + "/contacts/{contact_id}/notes":                               "contact.note",
		"POST " + tn + "/contacts/{contact_id}/notes":                              "contact.note",
		"PUT " + tn + "/contacts/{contact_id}/notes/{note_id}":                     "contact.note",
		"DELETE " + tn + "/contacts/{contact_id}/notes/{note_id}":                  "contact.note",
		// phase 06: the memory of the contact (earlier attendances, open follow-ups) and resolving an item
		"GET " + tn + "/inbox/conversations/{conversation_id}/attendance-context": "attendance.history",
		"GET " + tn + "/contacts/{contact_id}/attendance-history":                 "attendance.history",
		"GET " + tn + "/inbox/conversations/{conversation_id}/history-search":     "attendance.history",
		"POST " + tn + "/follow-ups/{follow_up_id}/resolve":                       "followup.resolve",
		// phase 07: the AI draft of the closing (summary and follow-ups); the handler also needs conversation.read, and only the holder gets it
		"POST " + tn + "/inbox/conversations/{conversation_id}/finalize/suggest": "ai.assist",
	}
	var problems []string
	for route, key := range want {
		if g, ok := got[route]; !ok {
			problems = append(problems, "missing: "+route)
		} else if g != key {
			problems = append(problems, route+" has key "+g+", want "+key)
		}
	}
	for route := range got {
		if _, ok := want[route]; !ok {
			problems = append(problems, "not reviewed: "+route+" ("+got[route]+")")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Errorf("the delegable routes differ from the reviewed list:\n  %s", strings.Join(problems, "\n  "))
	}
	// never a secret- or administration-class key on a delegated route
	for route, key := range got {
		for _, one := range strings.Split(key, "|") {
			for _, banned := range []string{"membership.", "channel.", "integration.", "billing.", "role.", "audit.", "settings."} {
				if strings.HasPrefix(one, banned) {
					t.Errorf("%s is delegable with the administrative key %s", route, one)
				}
			}
		}
	}
}
