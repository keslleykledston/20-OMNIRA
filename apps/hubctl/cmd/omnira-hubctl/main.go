// omnira-hubctl is the operator tool for Service Hub provisioning (hubs, members, contracts, grants).
//
// It connects with the same runtime database URL as the API (OMNIRA_DATABASE_URL, the unprivileged application role) and
// refuses to run as the database owner unless OMNIRA_ALLOW_PRIVILEGED_DB=true. Every change is audited with --operator.
// See docs/ops/HUB-PROVISIONING.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	osuser "os/user"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/worker/hubprojector"
)

const usage = `usage: omnira-hubctl --operator NAME <command> [flags]

  hub create       --name N [--description D]
  hub status       --hub ID --status active|suspended
  member add       --hub ID (--user ID | --email E) [--role hub_agent|hub_admin]   (default role: hub_agent)
  member remove    --hub ID (--user ID | --email E)
  contract create  --hub ID --tenant ID [--valid-until RFC3339] [--queues ID,ID,...]
  contract status  --hub ID --tenant ID --status active|suspended|revoked
  grant add        --hub ID --tenant ID (--user ID | --email E) [--valid-until RFC3339] [--reply]
                   (--reply lets the agent claim and answer; without it the grant is read-only, and renewing without it removes the capability)
  grant revoke     --hub ID --tenant ID (--user ID | --email E)
  platform-operator add|revoke  (--user ID | --email E)   (who may create companies/Hubs and switch them on and off; ADR-0038)
  platform-operator list
  show             --hub ID
  reconcile        (project every conversation into the Hub inbox once; the worker does this on a schedule when enabled)

Environment: OMNIRA_DATABASE_URL (application role).`

// command is the parsed, validated request. Parsing is pure so it is unit-tested without a database.
type command struct {
	operator, group, action  string
	hub, tenant, user        uuid.UUID
	email, name, description string
	role, status             string
	validUntil               *time.Time
	reply                    bool
	queues                   []uuid.UUID
}

var errUsage = errors.New("usage")

func parse(args []string) (command, error) {
	var c command
	fs := flag.NewFlagSet("omnira-hubctl", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.operator, "operator", "", "")
	if err := fs.Parse(args); err != nil {
		return c, fmt.Errorf("%w: %v", errUsage, err)
	}
	rest := fs.Args()
	if strings.TrimSpace(c.operator) == "" {
		return c, fmt.Errorf("%w: --operator is required (it is written to every audit event)", errUsage)
	}
	if len(rest) == 0 {
		return c, fmt.Errorf("%w: a command is required", errUsage)
	}
	c.group = rest[0]
	rest = rest[1:]
	if c.group != "show" && c.group != "reconcile" {
		if len(rest) == 0 {
			return c, fmt.Errorf("%w: %s needs an action", errUsage, c.group)
		}
		c.action = rest[0]
		rest = rest[1:]
	}
	var hub, tenant, user, valid, queues string
	sub := flag.NewFlagSet(c.group+" "+c.action, flag.ContinueOnError)
	sub.SetOutput(io.Discard)
	sub.StringVar(&hub, "hub", "", "")
	sub.StringVar(&tenant, "tenant", "", "")
	sub.StringVar(&user, "user", "", "")
	sub.StringVar(&c.email, "email", "", "")
	sub.StringVar(&c.name, "name", "", "")
	sub.StringVar(&c.description, "description", "", "")
	sub.StringVar(&c.role, "role", "", "")
	sub.StringVar(&c.status, "status", "", "")
	sub.StringVar(&valid, "valid-until", "", "")
	sub.StringVar(&queues, "queues", "", "")
	sub.BoolVar(&c.reply, "reply", false, "")
	if err := sub.Parse(rest); err != nil {
		return c, fmt.Errorf("%w: %v", errUsage, err)
	}
	if sub.NArg() > 0 {
		return c, fmt.Errorf("%w: unexpected argument %q", errUsage, sub.Arg(0))
	}
	id := func(label, v string, required bool) (uuid.UUID, error) {
		if v == "" {
			if required {
				return uuid.Nil, fmt.Errorf("%w: --%s is required", errUsage, label)
			}
			return uuid.Nil, nil
		}
		u, err := uuid.Parse(v)
		if err != nil || u == uuid.Nil {
			return uuid.Nil, fmt.Errorf("%w: --%s is not a valid id", errUsage, label)
		}
		return u, nil
	}
	var err error
	needHub := !(c.group == "hub" && c.action == "create") && c.group != "reconcile" && c.group != "platform-operator"
	needTenant := (c.group == "contract") || (c.group == "grant")
	needUser := (c.group == "member") || (c.group == "grant") || (c.group == "platform-operator" && c.action != "list")
	if c.hub, err = id("hub", hub, needHub); err != nil {
		return c, err
	}
	if c.tenant, err = id("tenant", tenant, needTenant); err != nil {
		return c, err
	}
	if c.user, err = id("user", user, false); err != nil {
		return c, err
	}
	if needUser && (c.user == uuid.Nil) == (c.email == "") {
		return c, fmt.Errorf("%w: give exactly one of --user or --email", errUsage)
	}
	if valid != "" {
		t, err := time.Parse(time.RFC3339, valid)
		if err != nil {
			return c, fmt.Errorf("%w: --valid-until must be RFC3339 (e.g. 2026-12-31T23:59:59Z)", errUsage)
		}
		c.validUntil = &t
	}
	if queues != "" {
		for _, part := range strings.Split(queues, ",") {
			q, err := uuid.Parse(strings.TrimSpace(part))
			if err != nil || q == uuid.Nil {
				return c, fmt.Errorf("%w: --queues must be a comma-separated list of ids", errUsage)
			}
			c.queues = append(c.queues, q)
		}
	}
	switch c.group + " " + c.action {
	case "hub create", "hub status", "member add", "member remove", "contract create", "contract status", "grant add", "grant revoke", "show ", "reconcile ", "platform-operator add", "platform-operator revoke", "platform-operator list":
	default:
		return c, fmt.Errorf("%w: unknown command %q", errUsage, strings.TrimSpace(c.group+" "+c.action))
	}
	if c.group == "hub" && c.action == "create" && strings.TrimSpace(c.name) == "" {
		return c, fmt.Errorf("%w: --name is required", errUsage)
	}
	if (c.group == "hub" || c.group == "contract") && c.action == "status" && c.status == "" {
		return c, fmt.Errorf("%w: --status is required", errUsage)
	}
	if c.group == "member" && c.action == "add" && c.role == "" {
		c.role = provisioning.RoleAgent
	}
	return c, nil
}

func main() {
	cmd, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", strings.TrimPrefix(err.Error(), "usage: "))
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := execute(ctx, cmd, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// osIdentity is the operating-system account and host that actually ran the tool. It is evidence, not proof: whoever
// can run this tool already holds the database credentials. It is recorded NEXT TO the claimed --operator name so a
// made-up name is at least visible in the audit trail.
func osIdentity() (account, host string) {
	if u, err := osuser.Current(); err == nil {
		account = u.Username
	}
	host, _ = os.Hostname()
	return account, host
}

// attributedOperator joins the claimed operator and the OS identity, keeping the whole string within the audit limit
// (the claimed name is shortened first, never the OS evidence).
func attributedOperator(claimed string, account, host string) string {
	const limit = 100
	clean := func(v string) string {
		v = strings.TrimSpace(v)
		if v == "" {
			return "?"
		}
		return strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return -1
			}
			return r
		}, v)
	}
	suffix := fmt.Sprintf(" [os %s@%s]", clean(account), clean(host))
	room := limit - len([]rune(suffix))
	name := []rune(strings.TrimSpace(claimed))
	if room < 1 {
		return string(name[:min(len(name), limit)])
	}
	if len(name) > room {
		name = name[:room]
	}
	return string(name) + suffix
}

func execute(ctx context.Context, c command, out io.Writer) error {
	url := os.Getenv("OMNIRA_DATABASE_URL")
	if url == "" {
		return errors.New("OMNIRA_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := platformdb.RequireUnprivilegedRole(ctx, pool); err != nil && os.Getenv("OMNIRA_ALLOW_PRIVILEGED_DB") != "true" {
		return fmt.Errorf("%w (set OMNIRA_ALLOW_PRIVILEGED_DB=true only for a deliberate exception)", err)
	}
	account, host := osIdentity()
	svc, err := provisioning.New(pool, attributedOperator(c.operator, account, host))
	if err != nil {
		return err
	}
	user := c.user
	if c.email != "" {
		if user, err = svc.FindUserByEmail(ctx, c.email); err != nil {
			return err
		}
	}
	switch c.group + " " + c.action {
	case "hub create":
		id, err := svc.CreateHub(ctx, c.name, c.description)
		if err == nil {
			fmt.Fprintf(out, "hub created: %s\n", id)
		}
		return err
	case "hub status":
		if err := svc.SetHubStatus(ctx, c.hub, c.status); err != nil {
			return err
		}
		fmt.Fprintf(out, "hub %s is now %s\n", c.hub, c.status)
	case "member add":
		if err := svc.AddMember(ctx, c.hub, user, c.role); err != nil {
			return err
		}
		fmt.Fprintf(out, "user %s is %s of hub %s (a member has NO access to any tenant until granted)\n", user, c.role, c.hub)
	case "member remove":
		n, err := svc.RemoveMember(ctx, c.hub, user)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "user %s removed from hub %s (%d grant(s) removed with it)\n", user, c.hub, n)
	case "contract create":
		id, err := svc.CreateContract(ctx, provisioning.ContractSpec{Hub: c.hub, Tenant: c.tenant, ValidUntil: c.validUntil, QueueIDs: c.queues})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "contract created: %s (hub %s -> tenant %s)\n", id, c.hub, c.tenant)
	case "contract status":
		if err := svc.SetContractStatus(ctx, c.hub, c.tenant, c.status); err != nil {
			return err
		}
		fmt.Fprintf(out, "contract hub %s -> tenant %s is now %s\n", c.hub, c.tenant, c.status)
	case "grant add":
		id, err := svc.Grant(ctx, provisioning.GrantSpec{Hub: c.hub, Tenant: c.tenant, User: user, ValidUntil: c.validUntil, CanReply: c.reply})
		if err != nil {
			return err
		}
		mode := "READ-ONLY"
		if c.reply {
			mode = "can claim and REPLY"
		}
		fmt.Fprintf(out, "grant %s: user %s may act on tenant %s through hub %s (%s)\n", id, user, c.tenant, c.hub, mode)
	case "grant revoke":
		if err := svc.RevokeGrant(ctx, c.hub, c.tenant, user); err != nil {
			return err
		}
		fmt.Fprintf(out, "grant revoked: user %s on tenant %s (hub %s)\n", user, c.tenant, c.hub)
	case "platform-operator add":
		if err := svc.AddPlatformOperator(ctx, user); err != nil {
			return err
		}
		fmt.Fprintf(out, "user %s is a platform operator\n", user)
	case "platform-operator revoke":
		if err := svc.RevokePlatformOperator(ctx, user); err != nil {
			return err
		}
		fmt.Fprintf(out, "user %s is no longer a platform operator\n", user)
	case "platform-operator list":
		ops, err := svc.ListPlatformOperators(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "platform operators (%d):\n", len(ops))
		for _, o := range ops {
			fmt.Fprintf(out, "  %-8s %s  %s  (granted by %s)\n", o.Status, o.UserID, o.Email, o.GrantedBy)
		}
	case "reconcile ":
		res, err := hubprojector.New(pool).ReconcileAll(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "projected: %d upserted, %d removed\n", res.Upserted, res.Removed)
	case "show ":
		s, err := svc.Describe(ctx, c.hub)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "hub %s  %q  [%s]\n", s.Hub.ID, s.Hub.Name, s.Hub.Status)
		fmt.Fprintf(out, "members (%d):\n", len(s.Members))
		for _, m := range s.Members {
			fmt.Fprintf(out, "  %-9s %s  %s\n", m.Role, m.UserID, m.Email)
		}
		fmt.Fprintf(out, "contracts (%d):\n", len(s.Contracts))
		for _, ct := range s.Contracts {
			scope := "all queues"
			if ct.Restricted {
				scope = fmt.Sprintf("%d queue(s)", len(ct.QueueIDs))
			}
			until := "no end"
			if ct.ValidUntil != nil {
				until = "until " + ct.ValidUntil.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(out, "  %s  %-9s %s, %s  %s\n", ct.TenantID, ct.Status, scope, until, ct.TenantName)
		}
		fmt.Fprintf(out, "grants (%d):\n", len(s.Grants))
		for _, g := range s.Grants {
			until := "no end"
			if g.ValidUntil != nil {
				until = "until " + g.ValidUntil.UTC().Format(time.RFC3339)
			}
			mode := "read-only"
			if g.CanReply {
				mode = "reply"
			}
			fmt.Fprintf(out, "  user %s -> tenant %s  %-9s %-9s %s\n", g.UserID, g.TenantID, g.Status, mode, until)
		}
	}
	return nil
}
