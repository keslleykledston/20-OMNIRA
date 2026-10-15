package adapters

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0048: every statement that locks a conversation to write an OUTBOUND message into it (FOR SHARE OF c) must also refuse a conversation an external
// application holds, so a person's reply (text, template, attachment) can never land in it, whatever races with the application's claim.
func TestEveryOutboundTargetExcludesAnExternalHold(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "outbound_system.go" {
			continue // the system sender asks for "free" (control_kind IS NULL), proven by the integration test
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for i := 0; ; {
			j := strings.Index(src[i:], "FOR SHARE OF c")
			if j < 0 {
				break
			}
			at := i + j
			open := strings.LastIndex(src[:at], "`")
			if open < 0 || !strings.Contains(src[open:at], "control_kind") {
				t.Errorf("%s: a write-target statement locks the conversation without refusing an application's hold", f)
			}
			checked++
			i = at + len("FOR SHARE OF c")
		}
	}
	if checked < 2 {
		t.Fatalf("expected the text and attachment insert targets, found %d", checked)
	}
}
