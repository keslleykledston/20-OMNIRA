package domain_test

import (
	"strings"
	"testing"

	. "github.com/omnira/omnira/internal/flows/domain"
)

// ADR-0049 phase 6: the validation of the five service-session nodes.

func svcDef(node string) string {
	return `{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},` + node + `,{"id":"fin","type":"end"}],"edges":[` +
		`{"id":"1","source":"start","sourcePort":"next","target":"n"},{"id":"2","source":"n","sourcePort":"next","target":"fin"}]}`
}

func issuesOf(t *testing.T, node string) []Issue {
	t.Helper()
	def, err := ParseDefinition([]byte(svcDef(node)))
	if err != nil {
		t.Fatal(err)
	}
	return Validate(def, ValidateOptions{})
}

func hasCode(is []Issue, code string) bool {
	for _, i := range is {
		if i.Code == code && i.Severity == SeverityError {
			return true
		}
	}
	return false
}

func TestServiceNodesAcceptValidConfigs(t *testing.T) {
	for name, node := range map[string]string{
		"start":             `{"id":"n","type":"start_service_session","config":{"variable":"protocolo"}}`,
		"start no variable": `{"id":"n","type":"start_service_session"}`,
		"classify":          `{"id":"n","type":"classify_service_session","config":{"category":"suporte-tecnico"}}`,
		"task":              `{"id":"n","type":"create_service_task","config":{"title":"Ligar para {{contact.name}}","priority":"high"}}`,
		"case":              `{"id":"n","type":"open_service_case","config":{"title":"Falha de link","variable":"ocorrencia"}}`,
		"close":             `{"id":"n","type":"close_service_session","config":{"result":"information_provided","note":"Dúvida respondida"}}`,
	} {
		for _, i := range issuesOf(t, node) {
			if i.Severity == SeverityError {
				t.Errorf("%s: unexpected error %s: %s", name, i.Code, i.Message)
			}
		}
	}
}

func TestServiceNodesRejectBadConfigs(t *testing.T) {
	long := strings.Repeat("a", MaxMessageRunes+1)
	cases := map[string]struct{ node, code string }{
		"category with spaces":   {`{"id":"n","type":"classify_service_session","config":{"category":"Suporte Técnico"}}`, "invalid_category"},
		"category missing":       {`{"id":"n","type":"classify_service_session","config":{}}`, "invalid_category"},
		"task without title":     {`{"id":"n","type":"create_service_task","config":{"title":"  "}}`, "missing_text"},
		"task bad priority":      {`{"id":"n","type":"create_service_task","config":{"title":"x","priority":"critical"}}`, "invalid_priority"},
		"task title too long":    {`{"id":"n","type":"create_service_task","config":{"title":"` + long + `"}}`, "text_too_long"},
		"case without title":     {`{"id":"n","type":"open_service_case","config":{}}`, "missing_text"},
		"case bad variable":      {`{"id":"n","type":"open_service_case","config":{"title":"x","variable":"Caso 1"}}`, "invalid_variable"},
		"start bad variable":     {`{"id":"n","type":"start_service_session","config":{"variable":"1x"}}`, "invalid_variable"},
		"close as abandoned":     {`{"id":"n","type":"close_service_session","config":{"result":"abandoned"}}`, "invalid_result"},
		"close without result":   {`{"id":"n","type":"close_service_session","config":{}}`, "invalid_result"},
		"close result uppercase": {`{"id":"n","type":"close_service_session","config":{"result":"Resolvido"}}`, "invalid_result"},
	}
	for name, c := range cases {
		if !hasCode(issuesOf(t, c.node), c.code) {
			t.Errorf("%s: want error %s, got %+v", name, c.code, issuesOf(t, c.node))
		}
	}
}

func TestServiceNodesRejectUnknownConfigFields(t *testing.T) {
	// a field the node does not have (an author trying to pass an assignee, a status) is refused, never silently ignored
	if !hasCode(issuesOf(t, `{"id":"n","type":"create_service_task","config":{"title":"x","assigned_user_id":"7f0b1e5e-0000-4000-8000-000000000001"}}`), "invalid_config") {
		t.Fatal("an unknown field must be an invalid_config")
	}
}

func TestServiceNodesDefineTheirVariablesAndReadTheirTemplates(t *testing.T) {
	def, err := ParseDefinition([]byte(`{"schema_version":1,"nodes":[{"id":"start","type":"trigger"},
	  {"id":"a","type":"start_service_session","config":{"variable":"protocolo"}},
	  {"id":"b","type":"create_service_task","config":{"title":"Retornar {{protocolo}}"}},
	  {"id":"fin","type":"end"}],"edges":[{"id":"1","source":"start","sourcePort":"next","target":"a"},{"id":"2","source":"a","sourcePort":"next","target":"b"},{"id":"3","source":"b","sourcePort":"next","target":"fin"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range Validate(def, ValidateOptions{}) {
		if i.Severity == SeverityError {
			t.Fatalf("a variable defined by start_service_session can be read afterwards: %+v", i)
		}
	}
}
