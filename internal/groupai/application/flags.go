package application

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/groupai/domain"
)

// Flags are the independent switches and limits of the group copilot (ADR-0060). Everything defaults to OFF: the code is ready, the
// rollout is a decision. Read once at startup from the environment, like the rest of OMNIRA.
type Flags struct {
	MemoryEnabled    bool // the memory, its API and the worker that keeps it
	ImportEnabled    bool // importing a conversation export
	ProposalsEnabled bool // drafting and approving cases and tasks from the memory
	// ImportAllowUnscanned lets an import through when no antivirus is configured. Off by default: without the antivirus nothing is accepted.
	ImportAllowUnscanned bool

	DailyCallsPerGroup int
	RawRetention       time.Duration
	ImportLocation     *time.Location
	// AutoAnalyze lets the worker read a group by itself (volume or age). Off = only when someone asks.
	AutoAnalyze bool
}

func DefaultFlags() Flags {
	loc, err := time.LoadLocation("America/Manaus")
	if err != nil {
		loc = time.UTC
	}
	return Flags{DailyCallsPerGroup: 48, RawRetention: domain.DefaultRawRetention, ImportLocation: loc}
}

func FlagsFromEnv(get func(string) string) Flags {
	if get == nil {
		get = os.Getenv
	}
	f := DefaultFlags()
	set := func(name string, dst *bool) {
		switch strings.ToLower(strings.TrimSpace(get(name))) {
		case "true", "1", "yes", "on":
			*dst = true
		case "false", "0", "no", "off":
			*dst = false
		}
	}
	set("OMNIRA_GROUP_MEMORY_ENABLED", &f.MemoryEnabled)
	set("OMNIRA_GROUP_IMPORT_ENABLED", &f.ImportEnabled)
	set("OMNIRA_GROUP_PROPOSALS_ENABLED", &f.ProposalsEnabled)
	set("OMNIRA_GROUP_IMPORT_ALLOW_UNSCANNED", &f.ImportAllowUnscanned)
	set("OMNIRA_GROUP_AUTO_ANALYZE", &f.AutoAnalyze)
	if v, err := strconv.Atoi(strings.TrimSpace(get("OMNIRA_GROUP_AI_DAILY_CALLS"))); err == nil && v > 0 && v <= 1000 {
		f.DailyCallsPerGroup = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(get("OMNIRA_GROUP_RAW_RETENTION_DAYS"))); err == nil && v >= 1 && v <= 3650 {
		f.RawRetention = time.Duration(v) * 24 * time.Hour
	}
	if tz := strings.TrimSpace(get("OMNIRA_GROUP_IMPORT_TZ")); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			f.ImportLocation = loc
		}
	}
	return f
}
