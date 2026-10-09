package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	groupsadapters "github.com/omnira/omnira/internal/groups/adapters"
	intelligenceadapters "github.com/omnira/omnira/internal/intelligence/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	"github.com/omnira/omnira/internal/platform/config"
)

// Drift guard between contracts/openapi/omnira-v1.yaml and the routes the server really registers.
// Forward: every documented operation must match a registered route (real ServeMux matching).
// Reverse: every inbox/channels/assign/messages route declared in server.go must be documented.

var (
	specPathRE   = regexp.MustCompile(`^  (/\S+):\s*$`)
	specMethodRE = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	paramRE      = regexp.MustCompile(`\{[^}]+\}`)
)

type op struct{ method, path string }

type contractOIDCHandler struct{}

func (contractOIDCHandler) Start(http.ResponseWriter, *http.Request)    {}
func (contractOIDCHandler) Callback(http.ResponseWriter, *http.Request) {}
func (contractOIDCHandler) Session(http.ResponseWriter, *http.Request)  {}
func (contractOIDCHandler) Logout(http.ResponseWriter, *http.Request)   {}

// specOps returns "METHOD /full/path" for every operation of the spec (server /api/v1 unless the
// path overrides `servers` with url "/").
func specOps(t *testing.T) []op {
	raw, err := os.ReadFile("../../../contracts/openapi/omnira-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var ops []op
	inPaths, cur, rootServer := false, "", false
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "paths:" {
			inPaths = true
			continue
		}
		if inPaths && line == "components:" {
			break
		}
		if !inPaths {
			continue
		}
		if m := specPathRE.FindStringSubmatch(line); m != nil {
			cur, rootServer = m[1], false
			continue
		}
		if cur != "" && strings.HasPrefix(line, "      - url: /") && strings.TrimSpace(line) == "- url: /" {
			rootServer = true
		}
		if m := specMethodRE.FindStringSubmatch(line); m != nil && cur != "" {
			full := "/api/v1" + cur
			if rootServer {
				full = cur
			}
			ops = append(ops, op{strings.ToUpper(m[1]), full})
		}
	}
	if len(ops) == 0 {
		t.Fatal("no operations parsed from the contract")
	}
	return ops
}

func newRoutedServer(t *testing.T) *Server {
	s := New("127.0.0.1:0")
	s.RegisterHealthHandlers()
	s.RegisterAuthHandlers(nil, false, nil, 0) // generates the RSA keys the other registrations need
	s.RegisterOIDCAuthHandlers(s.authenticator, nil, contractOIDCHandler{})
	s.RegisterMobileAuthHandlers(contractMobileHandler{})
	s.RegisterTenancyHandlers(nil, false)
	s.RegisterDeviceAdminHandlers(nil, contractDeviceAdminHandler{})
	s.RegisterInboxHandlers(nil, &config.Config{MediaDir: t.TempDir(), OutboundMediaEnabled: true, ClamAVAddr: "clamav:3310"})
	s.RegisterChannelManagementHandlers(nil, channeladapters.NewManagementHandler(nil), true)
	s.RegisterChannelDirectory(nil, channeladapters.NewDirectoryHandler(nil, nil))
	s.RegisterChannelTemplates(nil, channeladapters.NewTemplatesHandler(nil, nil, nil, nil))
	s.RegisterWahaConnectionHandlers(nil, channeladapters.NewConnectionHandler(nil))
	s.RegisterWahaWebhook(http.NotFoundHandler())
	s.RegisterGroupHandlers(nil, groupsadapters.NewHandler(nil, nil, nil))
	s.RegisterIntelligenceHandlers(nil, intelligenceadapters.NewTopicHandler(nil, nil, nil))
	s.RegisterAIIntegrationHandlers(nil, tenancyadapters.NewAIIntegrationHandler(nil, nil, nil))
	s.RegisterHubHandlers(nil, true, true)
	return s
}

func TestEveryDocumentedOperationIsARegisteredRoute(t *testing.T) {
	s := newRoutedServer(t)
	for _, o := range specOps(t) {
		concrete := paramRE.ReplaceAllString(o.path, "3f9c1b2e-0000-4000-8000-000000000001")
		_, pattern := s.mux.Handler(httptest.NewRequest(o.method, concrete, nil))
		if pattern == "" {
			t.Errorf("documented but not routed: %s %s", o.method, o.path)
		}
	}
}

func TestEveryInboxAndChannelRouteIsDocumented(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, o := range specOps(t) {
		documented[o.method+" "+paramRE.ReplaceAllString(o.path, "{}")] = true
	}
	var found []string
	// literal patterns: s.mux.Handle("METHOD /api/v1/tenants/{tenant_id}/inbox/...", ...)
	lit := regexp.MustCompile(`s\.mux\.Handle\("(GET|POST|PUT|PATCH|DELETE) (/api/v1/tenants/\{tenant_id\}/(?:inbox|channels)[^"]*)"`)
	for _, m := range lit.FindAllStringSubmatch(string(src), -1) {
		found = append(found, m[1]+" "+paramRE.ReplaceAllString(m[2], "{}"))
	}
	// the channels family uses: base := "..."; s.mux.Handle("METHOD "+base+"/suffix", ...)
	// Each base only owns the Handle calls that follow it, up to the next base
	// declaration — a cartesian product would invent routes that are not registered.
	baseRE := regexp.MustCompile(`base := "([^"]+)"`)
	bases := baseRE.FindAllStringSubmatchIndex(string(src), -1)
	if len(bases) == 0 {
		t.Fatal("channels base path not found in server.go")
	}
	cat := regexp.MustCompile(`s\.mux\.Handle\("(GET|POST) "\+base(?:\+"([^"]*)")?`)
	for i, base := range bases {
		end := len(src)
		if i+1 < len(bases) {
			end = bases[i+1][0]
		}
		basePath := string(src[base[2]:base[3]])
		for _, m := range cat.FindAllStringSubmatch(string(src[base[1]:end]), -1) {
			found = append(found, m[1]+" "+paramRE.ReplaceAllString(basePath+m[2], "{}"))
		}
	}
	if len(found) < 10 {
		t.Fatalf("parsed only %d routes from server.go — the extraction is broken", len(found))
	}
	sort.Strings(found)
	for _, f := range found {
		if !documented[f] {
			t.Errorf("routed but not documented in contracts/openapi/omnira-v1.yaml: %s", f)
		}
	}
}

// IAM3 MVP: system roles are fixed and there is no role editor. The legacy /members API
// (no permission check, unrestricted role_id) was removed. None of these may come back
// without an explicit decision.
func TestNoRoleEditingOrLegacyMembersRoutes(t *testing.T) {
	s := newRoutedServer(t)
	tenant := "3f9c1b2e-0000-4000-8000-000000000001"
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/tenants/" + tenant + "/roles"},
		{http.MethodPatch, "/api/v1/tenants/" + tenant + "/roles/" + tenant},
		{http.MethodPut, "/api/v1/tenants/" + tenant + "/roles/" + tenant},
		{http.MethodDelete, "/api/v1/tenants/" + tenant + "/roles/" + tenant},
		{http.MethodGet, "/api/v1/tenants/" + tenant + "/members"},
		{http.MethodPost, "/api/v1/tenants/" + tenant + "/members"},
		{http.MethodDelete, "/api/v1/tenants/" + tenant + "/members/" + tenant},
	} {
		if _, pattern := s.mux.Handler(httptest.NewRequest(c.method, c.path, nil)); pattern != "" {
			t.Errorf("%s %s must not be routed (matched %q)", c.method, c.path, pattern)
		}
	}
}

// contractMobileHandler stands in for authn.MobileHandler (the contract test only needs the routes to exist).
type contractMobileHandler struct{}

func (contractMobileHandler) Token(http.ResponseWriter, *http.Request)       {}
func (contractMobileHandler) Refresh(http.ResponseWriter, *http.Request)     {}
func (contractMobileHandler) Logout(http.ResponseWriter, *http.Request)      {}
func (contractMobileHandler) ListDevices(http.ResponseWriter, *http.Request) {}
func (contractMobileHandler) DeleteDevice(http.ResponseWriter, *http.Request) {}

type contractDeviceAdminHandler struct{}

func (contractDeviceAdminHandler) List(http.ResponseWriter, *http.Request)   {}
func (contractDeviceAdminHandler) Revoke(http.ResponseWriter, *http.Request) {}
