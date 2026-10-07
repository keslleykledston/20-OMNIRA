package adapters

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// DeviceAdminHandler lets an administrator see and revoke the app installations of ANOTHER user (ADR-0022, decision 3).
//
// An installation belongs to the user and works in every tenant the user is a member of, while membership.manage is a permission of ONE
// tenant. So the administrator needs membership.manage in EVERY tenant where the target is an active member; otherwise an admin of tenant A
// could cut a user off from tenant B. When that is not the case the answer is 403 and the tool left to the admin is the one they always
// had: deactivate or revoke the membership in their own tenant (effective on the next request).
type DeviceAdminHandler struct {
	pool    *pgxpool.Pool
	devices authn.DeviceStore
	audit   auditports.AuditEventRepository
}

func NewDeviceAdminHandler(pool *pgxpool.Pool, devices authn.DeviceStore, audit auditports.AuditEventRepository) *DeviceAdminHandler {
	return &DeviceAdminHandler{pool: pool, devices: devices, audit: audit}
}

var errNotAllTenants = errors.New("membership.manage is missing in some tenant of the target")

// resolveTarget returns the user behind {membership_id} in the path tenant after checking the actor's authority. The membership lookup runs
// under the actor's RLS session, so a membership of another tenant and a nonexistent id answer the same 404.
func (h *DeviceAdminHandler) resolveTarget(r *http.Request) (tc *domain.TenantContext, target uuid.UUID, status int, err error) {
	team := &TeamHandler{pool: h.pool}
	tc, err = team.authorize(r, permissionMembershipManage)
	if err != nil {
		if errors.Is(err, errPermissionDenied) {
			return nil, uuid.Nil, http.StatusForbidden, err
		}
		return nil, uuid.Nil, http.StatusInternalServerError, err
	}
	membershipID, perr := uuid.Parse(r.PathValue("membership_id"))
	if perr != nil {
		return nil, uuid.Nil, http.StatusBadRequest, perr
	}
	qerr := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT user_id FROM memberships WHERE tenant_id=$1 AND id=$2`, tc.TenantID, membershipID).Scan(&target)
	if errors.Is(qerr, pgx.ErrNoRows) {
		return nil, uuid.Nil, http.StatusNotFound, qerr
	}
	if qerr != nil {
		return nil, uuid.Nil, http.StatusInternalServerError, qerr
	}
	if target == tc.ActorID {
		// Own installations are managed through /me/devices.
		return nil, uuid.Nil, http.StatusUnprocessableEntity, errors.New("use /me/devices for your own installations")
	}
	if err := h.actorManagesEveryTenantOf(r.Context(), tc.ActorID, target); err != nil {
		if errors.Is(err, errNotAllTenants) {
			return nil, uuid.Nil, http.StatusForbidden, err
		}
		return nil, uuid.Nil, http.StatusInternalServerError, err
	}
	return tc, target, 0, nil
}

// actorManagesEveryTenantOf is evaluated in system mode on purpose: under the actor's own RLS session the target's memberships in tenants the
// actor does not belong to would be invisible, which is exactly the case that must be refused.
func (h *DeviceAdminHandler) actorManagesEveryTenantOf(ctx context.Context, actor, target uuid.UUID) error {
	var missing int
	err := platformdb.WithTenantSession(ctx, h.pool, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, h.pool).QueryRow(scoped, `
			SELECT count(*) FROM memberships t
			WHERE t.user_id=$2 AND t.status='active'
			  AND NOT EXISTS (
			    SELECT 1 FROM memberships a JOIN role_permissions rp ON rp.role_id=a.role_id
			    WHERE a.tenant_id=t.tenant_id AND a.user_id=$1 AND a.status='active' AND rp.permission_key=$3)`,
			actor, target, permissionMembershipManage).Scan(&missing)
	})
	if err != nil {
		return err
	}
	if missing > 0 {
		return errNotAllTenants
	}
	return nil
}

func (h *DeviceAdminHandler) fail(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusForbidden:
		http.Error(w, "forbidden", status)
	case http.StatusNotFound:
		http.Error(w, "membership not found", status)
	case http.StatusBadRequest:
		http.Error(w, "invalid id", status)
	case http.StatusUnprocessableEntity:
		http.Error(w, "use /me/devices for your own installations", status)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

// List — GET /api/v1/tenants/{tenant_id}/team/{membership_id}/devices
func (h *DeviceAdminHandler) List(w http.ResponseWriter, r *http.Request) {
	_, target, status, err := h.resolveTarget(r)
	if err != nil {
		h.fail(w, status)
		return
	}
	devices, err := h.devices.ListDevices(r.Context(), target, uuid.Nil)
	if err != nil {
		http.Error(w, "devices unavailable", http.StatusServiceUnavailable)
		return
	}
	writeTeamJSON(w, map[string]any{"items": devices})
}

// Revoke — DELETE /api/v1/tenants/{tenant_id}/team/{membership_id}/devices/{device_id}
func (h *DeviceAdminHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	tc, target, status, err := h.resolveTarget(r)
	if err != nil {
		h.fail(w, status)
		return
	}
	deviceID, err := uuid.Parse(r.PathValue("device_id"))
	if err != nil {
		h.fail(w, http.StatusBadRequest)
		return
	}
	if err := h.devices.RevokeDevice(r.Context(), target, deviceID, authn.RevokedAdmin); err != nil {
		if errors.Is(err, authn.ErrDeviceNotFound) {
			http.Error(w, "device not found", http.StatusNotFound)
			return
		}
		http.Error(w, "devices unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.audit != nil {
		_ = h.audit.Store(r.Context(), &auditdomain.AuditEvent{
			ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID, Action: auditdomain.ActionDeviceRevokedByAdmin,
			ResourceType: auditdomain.ResourceDevice, ResourceID: deviceID,
			Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
			Metadata: map[string]interface{}{"target_user_id": target.String()}, CreatedAt: time.Now().UTC(),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}
