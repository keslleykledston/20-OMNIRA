package jobsstream

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// streamMutationPattern matches the exact call shapes that configure a
// JetStream stream's policy (as opposed to CreateOrUpdateConsumer, which
// only manages a durable consumer and is fine anywhere).
var streamMutationPattern = regexp.MustCompile(`\bjs\.(CreateOrUpdateStream|CreateStream|UpdateStream)\(`)

// TestSingleCanonicalStreamMutationCallSite is a repository-wide,
// compile-independent proof (PILOT.4D3-C1) that exactly one place in the
// non-test source tree is allowed to configure OMNIRA_JOBS' stream policy:
// internal/worker/jobsstream itself. Routing and delivery consumers lost
// their own CreateOrUpdateStream calls in this same change specifically so
// this invariant holds by construction, not by convention — this test is
// what keeps a future edit from silently reintroducing a second call site.
func TestSingleCanonicalStreamMutationCallSite(t *testing.T) {
	root := moduleRoot(t)

	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if streamMutationPattern.Match(data) {
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking repository: %v", err)
	}

	const canonical = "internal/worker/jobsstream/config.go"
	if len(hits) != 1 || hits[0] != canonical {
		t.Fatalf("stream-policy mutation call sites = %v, want exactly [%s]", hits, canonical)
	}
}

// moduleRoot walks up from the current test's working directory until it
// finds go.mod — this test must work regardless of which directory `go
// test` is invoked from.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate module root (go.mod) walking up from test working directory")
		}
		dir = parent
	}
}
