package delivery_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/external/adapters"
	"github.com/omnira/omnira/internal/external/delivery"
)

// The contract of the events (AsyncAPI + JSON Schema + samples) cannot drift from the code: every sample satisfies the schema, every type of the journal is described
// and sampled, the samples decode strictly into the envelope the API really sends, nothing in them is personal data, and the signed sample verifies with the code that signs.

const contracts = "../../../contracts/"

func readJSON(t *testing.T, path string) any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

// validate implements the subset of JSON Schema the contract uses: type, enum, const, required, properties, additionalProperties:false, items, minimum, format (uuid,
// date-time), $ref to #/$defs and oneOf. It returns every violation.
func validate(root, schema, v any, path string, errs *[]string) {
	m := schema.(map[string]any)
	if ref, ok := m["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		validate(root, root.(map[string]any)["$defs"].(map[string]any)[name], v, path, errs)
		return
	}
	if alts, ok := m["oneOf"].([]any); ok {
		matches := 0
		for _, a := range alts {
			var sub []string
			validate(root, a, v, path, &sub)
			if len(sub) == 0 {
				matches++
			}
		}
		if matches != 1 {
			*errs = append(*errs, fmt.Sprintf("%s: matches %d of the alternatives, want exactly 1", path, matches))
		}
		return
	}
	if c, ok := m["const"]; ok && fmt.Sprint(c) != fmt.Sprint(v) {
		*errs = append(*errs, fmt.Sprintf("%s: %v is not %v", path, v, c))
	}
	if e, ok := m["enum"].([]any); ok {
		found := false
		for _, x := range e {
			if fmt.Sprint(x) == fmt.Sprint(v) {
				found = true
			}
		}
		if !found {
			*errs = append(*errs, fmt.Sprintf("%s: %v is not one of %v", path, v, e))
		}
	}
	if ty, ok := m["type"]; ok {
		types := []string{}
		switch x := ty.(type) {
		case string:
			types = append(types, x)
		case []any:
			for _, a := range x {
				types = append(types, a.(string))
			}
		}
		okType := false
		for _, want := range types {
			switch want {
			case "object":
				_, okType = v.(map[string]any)
			case "array":
				_, okType = v.([]any)
			case "string":
				_, okType = v.(string)
			case "integer":
				f, isNum := v.(float64)
				okType = isNum && f == float64(int64(f))
			case "boolean":
				_, okType = v.(bool)
			case "null":
				okType = v == nil
			}
			if okType {
				break
			}
		}
		if !okType {
			*errs = append(*errs, fmt.Sprintf("%s: %v is not of type %v", path, v, types))
			return
		}
	}
	if f, ok := m["format"].(string); ok {
		if s, isStr := v.(string); isStr {
			switch f {
			case "uuid":
				if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(s) {
					*errs = append(*errs, path+": not a uuid")
				}
			case "date-time":
				if _, err := time.Parse(time.RFC3339, s); err != nil {
					*errs = append(*errs, path+": not a date-time")
				}
			}
		}
	}
	if min, ok := m["minimum"].(float64); ok {
		if f, isNum := v.(float64); isNum && f < min {
			*errs = append(*errs, fmt.Sprintf("%s: %v < %v", path, f, min))
		}
	}
	if obj, isObj := v.(map[string]any); isObj {
		props, _ := m["properties"].(map[string]any)
		if req, ok := m["required"].([]any); ok {
			for _, r := range req {
				if _, present := obj[r.(string)]; !present {
					*errs = append(*errs, fmt.Sprintf("%s: missing %q", path, r))
				}
			}
		}
		for k, val := range obj {
			if sub, known := props[k]; known {
				validate(root, sub, val, path+"."+k, errs)
			} else if ap, ok := m["additionalProperties"].(bool); ok && !ap {
				*errs = append(*errs, fmt.Sprintf("%s: unexpected property %q", path, k))
			}
		}
	}
	if arr, isArr := v.([]any); isArr {
		if items, ok := m["items"]; ok {
			for i, x := range arr {
				validate(root, items, x, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	}
}

func path0(string) string { return "sample" }

func journalTypes(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../../migrations/000137_domain_events.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`event_type TEXT NOT NULL CHECK \(event_type IN \(([^)]*)\)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("the event type list of the journal was not found")
	}
	var out []string
	for _, x := range regexp.MustCompile(`'([a-z_.0-9]+)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, x[1])
	}
	sort.Strings(out)
	return out
}

func TestEverySampleSatisfiesTheSchemaAndEveryJournalTypeIsDescribed(t *testing.T) {
	schema := readJSON(t, contracts+"jsonschema/external-event.v1.schema.json")
	types := journalTypes(t)
	if len(types) != 7 {
		t.Fatalf("journal types = %v", types)
	}
	asyncRaw, err := os.ReadFile(contracts + "asyncapi/omnira-external-events-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range types {
		if !strings.Contains(string(asyncRaw), "name: "+ty) {
			t.Errorf("AsyncAPI does not describe %s", ty)
		}
		if !strings.Contains(string(asyncRaw), "event-"+ty+".json") {
			t.Errorf("AsyncAPI does not link the sample of %s", ty)
		}
		path := contracts + "samples/external/event-" + ty + ".json"
		sample := readJSON(t, path)
		var errs []string
		validate(schema, schema, sample, "$", &errs)
		if len(errs) > 0 {
			t.Errorf("%s does not satisfy the schema: %v", path, errs)
		}
		// and it is exactly the envelope the API sends (strict decoding: no field the code does not know)
		raw, _ := os.ReadFile(path)
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		var dto adapters.EventDTO
		if err := dec.Decode(&dto); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		if dto.Type != ty {
			t.Errorf("%s carries type %s", path, dto.Type)
		}
		// no personal data in an envelope: no key that carries content or identity, no phone-shaped value
		var tree any
		_ = json.Unmarshal(raw, &tree)
		var walk func(path string, v any)
		walk = func(path string, v any) {
			switch x := v.(type) {
			case map[string]any:
				for k, val := range x {
					switch k {
					case "body", "text", "phone", "phone_e164", "email", "name", "display_name", "whatsapp_name", "subject", "caption":
						t.Errorf("%s: %s.%s - an envelope never carries content or identity", path0(path), path, k)
					}
					walk(path+"."+k, val)
				}
			case []any:
				for i, val := range x {
					walk(fmt.Sprintf("%s[%d]", path, i), val)
				}
			case string:
				isID := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(x)
				if _, tErr := time.Parse(time.RFC3339, x); !isID && tErr != nil && regexp.MustCompile(`\+?\d{10,}`).MatchString(x) {
					t.Errorf("%s: a phone-shaped value at %s", path0(path), path)
				}
			}
		}
		walk("$", tree)
	}
	// the schema itself describes exactly these types
	alts := schema.(map[string]any)["oneOf"].([]any)
	var described []string
	for _, a := range alts {
		described = append(described, a.(map[string]any)["properties"].(map[string]any)["type"].(map[string]any)["const"].(string))
	}
	sort.Strings(described)
	if strings.Join(described, ",") != strings.Join(types, ",") {
		t.Errorf("schema describes %v, the journal has %v", described, types)
	}
	// a sample that breaks the contract is caught (the validator is not a rubber stamp)
	bad := readJSON(t, contracts+"samples/external/event-message.created.v1.json").(map[string]any)
	bad["data"].(map[string]any)["body"] = "senha 123"
	var errs []string
	validate(schema, schema, bad, "$", &errs)
	if len(errs) == 0 {
		t.Error("a message fact carrying a body satisfied the schema")
	}
}

func TestTheSignedSampleVerifiesWithTheCodeThatSigns(t *testing.T) {
	var d struct {
		Secret  string `json:"secret"`
		Request struct {
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"request"`
	}
	raw, err := os.ReadFile(filepath.Join(contracts, "samples/external/webhook-delivery.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	h := d.Request.Headers
	var ts int64
	fmt.Sscan(h["X-Omnira-Timestamp"], &ts)
	now := time.Unix(ts, 0).Add(30 * time.Second)
	if !delivery.Verify([]byte(d.Secret), h["X-Omnira-Signature"], h["X-Omnira-Timestamp"], []byte(d.Request.Body), now) {
		t.Fatal("the published sample does not verify")
	}
	if delivery.Verify([]byte(d.Secret), h["X-Omnira-Signature"], h["X-Omnira-Timestamp"], []byte(d.Request.Body+" "), now) {
		t.Error("a modified body verified")
	}
	if delivery.Verify([]byte(d.Secret), h["X-Omnira-Signature"], h["X-Omnira-Timestamp"], []byte(d.Request.Body), now.Add(time.Hour)) {
		t.Error("a replayed sample verified an hour later")
	}
	// the sample body is itself a valid envelope
	schema := readJSON(t, contracts+"jsonschema/external-event.v1.schema.json")
	var body any
	_ = json.Unmarshal([]byte(d.Request.Body), &body)
	var errs []string
	validate(schema, schema, body, "$", &errs)
	if len(errs) > 0 {
		t.Errorf("the sample body breaks the schema: %v", errs)
	}
	if h["X-Omnira-Event-ID"] == "" || h["X-Omnira-Delivery-ID"] == "" || h["X-Omnira-Key-ID"] == "" {
		t.Error("the sample lacks the identifying headers")
	}
}
