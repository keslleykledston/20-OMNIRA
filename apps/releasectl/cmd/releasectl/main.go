// omnira-releasectl builds and validates release manifests (ADR-0054, F3).
//
// It is a thin, pure CLI over internal/platformrelease/domain: no HTTP, no
// database, no Docker socket, no network call of its own (release-build.yml
// calls docker/cosign/GHCR directly as separate steps; this tool only ever
// touches the manifest JSON). build-manifest ALWAYS re-parses what it just
// built through the exact same strict ParseManifest used at verification
// time (dogfooding the verifier) — a builder that could produce a manifest
// its own verifier would reject is a worse bug than not building one at all.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

const usage = `usage: omnira-releasectl <command> [flags]

  build-manifest    assemble a release manifest from flags, validate it
                     through the same verifier a consumer would use, and
                     write the canonical JSON to --out (or stdout)
  validate-manifest  --file PATH : parse and validate an existing manifest;
                     exits 0 and prints nothing on success, exits 1 and
                     prints the exact rejection reason on failure

build-manifest flags:
  --release-id STRING          e.g. omnira-2.4.0
  --platform-version STRING    e.g. 2.4.0
  --channel STRING             dev|candidate|stable
  --repository STRING          e.g. keslleykledston/20-OMNIRA
  --commit-sha STRING          full 40-hex git SHA
  --workflow-run-id STRING
  --artifact UNIT=IMAGE        repeatable, e.g. --artifact omnira-api=ghcr.io/x/omnira-api@sha256:...
  --module STRING              repeatable; module_id from release/components.yaml
  --from-version STRING        repeatable; compatibility.from_versions
  --min-agent-protocol INT
  --database-strategy STRING   expand_then_contract_later|expand_only|no_schema_change
  --migrations-start-after STRING
  --migrations-target STRING
  --migrations-checksum STRING sha256:<64 hex>
  --rollout-workload STRING    repeatable
  --preflight-policy STRING    default: strict
  --recovery-class STRING      R0|R1|R2|R3|R4
  --release-notes-ref STRING   optional
  --approved-by STRING
  --out PATH                   default: stdout
`

// stringSliceFlag accumulates a repeatable -flag value=value2=... into a slice —
// flag.Value, Go's standard extension point for a flag the default package has no
// built-in type for.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "build-manifest":
		err = runBuildManifest(os.Args[2:], os.Stdout)
	case "validate-manifest":
		err = runValidateManifest(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "omnira-releasectl:", err)
		os.Exit(1)
	}
}

func runValidateManifest(args []string) error {
	fs := flag.NewFlagSet("validate-manifest", flag.ContinueOnError)
	file := fs.String("file", "", "path to the manifest JSON to validate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return errors.New("validate-manifest: --file is required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("validate-manifest: %w", err)
	}
	if _, err := domain.ParseManifest(raw); err != nil {
		return err // domain.ParseManifest's own error already names the exact field/reason
	}
	return nil
}

func runBuildManifest(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("build-manifest", flag.ContinueOnError)
	releaseID := fs.String("release-id", "", "")
	platformVersion := fs.String("platform-version", "", "")
	channel := fs.String("channel", "", "")
	repository := fs.String("repository", "", "")
	commitSHA := fs.String("commit-sha", "", "")
	workflowRunID := fs.String("workflow-run-id", "", "")
	var artifacts stringSliceFlag
	fs.Var(&artifacts, "artifact", "")
	var modules stringSliceFlag
	fs.Var(&modules, "module", "")
	var fromVersions stringSliceFlag
	fs.Var(&fromVersions, "from-version", "")
	minAgentProtocol := fs.Int("min-agent-protocol", 1, "")
	databaseStrategy := fs.String("database-strategy", "", "")
	migrationsStartAfter := fs.String("migrations-start-after", "", "")
	migrationsTarget := fs.String("migrations-target", "", "")
	migrationsChecksum := fs.String("migrations-checksum", "", "")
	var rolloutWorkloads stringSliceFlag
	fs.Var(&rolloutWorkloads, "rollout-workload", "")
	preflightPolicy := fs.String("preflight-policy", "strict", "")
	recoveryClass := fs.String("recovery-class", "", "")
	releaseNotesRef := fs.String("release-notes-ref", "", "")
	approvedBy := fs.String("approved-by", "", "")
	outPath := fs.String("out", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	m := domain.ReleaseManifest{
		SchemaVersion:   domain.SupportedSchemaVersion,
		ReleaseID:       *releaseID,
		PlatformVersion: *platformVersion,
		Channel:         domain.Channel(*channel),
		Source: domain.Source{
			Repository:    *repository,
			CommitSHA:     *commitSHA,
			WorkflowRunID: *workflowRunID,
		},
		ModulesChanged: []string(modules),
		Compatibility: domain.Compatibility{
			FromVersions:     []string(fromVersions),
			MinAgentProtocol: *minAgentProtocol,
			DatabaseStrategy: *databaseStrategy,
		},
		Migrations: domain.Migrations{
			StartAfter:     *migrationsStartAfter,
			Target:         *migrationsTarget,
			ChecksumBundle: *migrationsChecksum,
		},
		Rollout: domain.Rollout{
			PreflightPolicy: *preflightPolicy,
			RecoveryClass:   domain.RecoveryClass(*recoveryClass),
		},
		ReleaseNotesRef: *releaseNotesRef,
		ApprovedBy:      *approvedBy,
	}
	for _, w := range rolloutWorkloads {
		m.Rollout.Workloads = append(m.Rollout.Workloads, domain.BuildUnit(w))
	}
	for _, a := range artifacts {
		unit, image, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("build-manifest: --artifact %q must be UNIT=IMAGE", a)
		}
		m.Artifacts = append(m.Artifacts, domain.Artifact{Unit: domain.BuildUnit(unit), Image: image})
	}
	// modules_changed must be present-but-possibly-empty, never omitted, per
	// the schema's own required list (ADR-0054, F1) — an explicit [] when no
	// --module flag was given, never a nil/absent field.
	if m.ModulesChanged == nil {
		m.ModulesChanged = []string{}
	}
	if m.Compatibility.FromVersions == nil {
		m.Compatibility.FromVersions = []string{}
	}

	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("build-manifest: encoding: %w", err)
	}
	// Dogfood the verifier: a manifest this tool cannot itself re-parse and
	// validate is never written anywhere, regardless of which flag was
	// missing or malformed.
	if _, err := domain.ParseManifest(encoded); err != nil {
		return fmt.Errorf("build-manifest: assembled manifest fails its own verifier (this is a bug in the flags given, not in the verifier): %w", err)
	}

	if *outPath == "" {
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	return os.WriteFile(*outPath, append(encoded, '\n'), 0o644)
}
