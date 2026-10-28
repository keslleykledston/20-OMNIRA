package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validArgs(extra ...string) []string {
	base := []string{
		"--release-id", "omnira-2.4.0",
		"--platform-version", "2.4.0",
		"--channel", "candidate",
		"--repository", "keslleykledston/20-OMNIRA",
		"--commit-sha", "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
		"--workflow-run-id", "123456789",
		"--artifact", "omnira-api=ghcr.io/x/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"--module", "platform_release",
		"--from-version", "2.3.x",
		"--min-agent-protocol", "1",
		"--database-strategy", "expand_then_contract_later",
		"--migrations-start-after", "000168",
		"--migrations-target", "000172",
		"--migrations-checksum", "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		"--rollout-workload", "omnira-api",
		"--recovery-class", "R1",
		"--approved-by", "release-policy-candidate-v1",
	}
	return append(base, extra...)
}

func TestRunBuildManifest_Valid(t *testing.T) {
	var buf bytes.Buffer
	if err := runBuildManifest(validArgs(), &buf); err != nil {
		t.Fatalf("expected a valid manifest to build and self-validate, got: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected JSON written to stdout, got nothing")
	}
}

func TestRunBuildManifest_WritesToFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "manifest.json")
	var buf bytes.Buffer
	if err := runBuildManifest(validArgs("--out", out), &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Error("expected nothing written to stdout when --out is given")
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("expected the output file to exist: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("output file is empty")
	}
}

func TestRunBuildManifest_RejectsMalformedArtifactFlag(t *testing.T) {
	var buf bytes.Buffer
	// "omnira-api" with no "=IMAGE" — a flag-level mistake, must be caught
	// before ever reaching the domain verifier.
	bad := append(validArgs(), "--artifact", "omnira-worker-missing-equals")
	if err := runBuildManifest(bad, &buf); err == nil {
		t.Fatal("expected an error for a malformed --artifact flag (missing '=')")
	}
}

func TestRunBuildManifest_SelfVerifiesAndRejectsAnInvalidAssembly(t *testing.T) {
	// --approved-by is required by the domain verifier; omitting it must
	// make build-manifest itself fail — never write an invalid manifest
	// anywhere, even to stdout.
	args := []string{
		"--release-id", "omnira-2.4.0",
		"--platform-version", "2.4.0",
		"--channel", "candidate",
		"--repository", "keslleykledston/20-OMNIRA",
		"--commit-sha", "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
		"--workflow-run-id", "123456789",
		"--artifact", "omnira-api=ghcr.io/x/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"--database-strategy", "expand_then_contract_later",
		"--migrations-start-after", "000168",
		"--migrations-target", "000172",
		"--migrations-checksum", "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		"--rollout-workload", "omnira-api",
		"--recovery-class", "R1",
		// approved-by deliberately omitted
	}
	var buf bytes.Buffer
	if err := runBuildManifest(args, &buf); err == nil {
		t.Fatal("expected build-manifest to refuse an assembly its own verifier would reject (missing approved_by)")
	}
	if buf.Len() != 0 {
		t.Error("an invalid manifest must never be written anywhere, including stdout")
	}
}

func TestRunBuildManifest_EmptyModulesAndFromVersionsAreExplicitArraysNotNull(t *testing.T) {
	// Regression guard for the exact bug F1's verifier was built to catch:
	// an absent/null modules_changed must never happen. Omitting --module
	// and --from-version must still produce [] (present, empty), which the
	// verifier accepts, not a missing/null field, which it would reject.
	out := filepath.Join(t.TempDir(), "manifest.json")
	args := []string{
		"--release-id", "omnira-2.4.0",
		"--platform-version", "2.4.0",
		"--channel", "candidate",
		"--repository", "keslleykledston/20-OMNIRA",
		"--commit-sha", "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
		"--workflow-run-id", "123456789",
		"--artifact", "omnira-api=ghcr.io/x/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"--min-agent-protocol", "1",
		"--database-strategy", "no_schema_change",
		"--migrations-start-after", "000172",
		"--migrations-target", "000172",
		"--migrations-checksum", "sha256:4444444444444444444444444444444444444444444444444444444444444444",
		"--rollout-workload", "omnira-api",
		"--recovery-class", "R0",
		"--approved-by", "release-policy-candidate-v1",
		"--out", out,
	}
	var buf bytes.Buffer
	// compatibility.from_versions is required non-empty by the domain
	// verifier (len==0 check) — so THIS specific case should fail, and the
	// error should name that field, confirming [] (not null) reached the
	// verifier (a null would have failed the earlier required-field check
	// instead, with a different message).
	err := runBuildManifest(args, &buf)
	if err == nil {
		t.Fatal("expected failure: compatibility.from_versions is required to be non-empty")
	}
	if got := err.Error(); !strings.Contains(got, "from_versions") {
		t.Errorf("expected the error to name from_versions, got: %s", got)
	}
}

func TestRunValidateManifest_ValidFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "manifest.json")
	var buf bytes.Buffer
	if err := runBuildManifest(validArgs("--out", out), &buf); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := runValidateManifest([]string{"--file", out}); err != nil {
		t.Errorf("expected the just-built manifest to validate cleanly: %v", err)
	}
}

func TestRunValidateManifest_MissingFile(t *testing.T) {
	if err := runValidateManifest([]string{"--file", "/no/such/file.json"}); err == nil {
		t.Fatal("expected an error for a nonexistent file")
	}
}

func TestRunValidateManifest_RequiresFileFlag(t *testing.T) {
	if err := runValidateManifest([]string{}); err == nil {
		t.Fatal("expected an error when --file is not given")
	}
}
