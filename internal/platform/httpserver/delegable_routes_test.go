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
	const tn = "/api/v1/tenants/{tenant_id}"
	want := map[string]string{
		// phase 03: attend the conversation
		"GET " + tn + "/me/access":                                                "conversation.read",
		"GET " + tn + "/inbox/conversations":                                      "conversation.read",
		"GET " + tn + "/inbox/conversations/{conversation_id}":                    "conversation.read",
		"GET " + tn + "/inbox/conversations/{conversation_id}/messages":           "conversation.read",
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
