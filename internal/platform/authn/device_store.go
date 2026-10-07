package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device sessions for native clients (ADR-0022). An app installation ("device") owns one refresh chain ("family"):
//
//   - access token: opaque, 15 minutes, resolved server side on every request (so revocation is immediate);
//   - refresh token: opaque, single use, 30 days sliding but never beyond the family's 90-day absolute limit;
//   - only SHA-256 digests are stored; the plain values exist once, in the response that issues them.
//
// Rotation and revocation both take the family row lock, so a revocation that committed can never be followed by a live successor.

const (
	AccessTokenPrefix  = "omn_at_"
	RefreshTokenPrefix = "omn_rt_"

	AccessTokenTTL      = 15 * time.Minute
	RefreshTokenTTL     = 30 * 24 * time.Hour
	FamilyAbsoluteTTL   = 90 * 24 * time.Hour
	maxActiveDevices    = 20
	deviceLastSeenSlack = time.Minute
)

// Revocation reasons (stored; keep in sync with the CHECK in migration 000090).
const (
	RevokedLogout   = "logout"
	RevokedUser     = "user"
	RevokedAdmin    = "admin"
	RevokedReuse    = "reuse"
	RevokedReplaced = "replaced"
	RevokedLimit    = "limit"
)

var (
	// ErrInvalidToken is the single answer for an unknown, expired, used or revoked credential: callers must not learn which.
	ErrInvalidToken = errors.New("invalid or expired token")
	// ErrRefreshReuse means a refresh token that was already spent came back: the whole family is revoked (the caller still answers 401).
	ErrRefreshReuse   = errors.New("refresh token reuse detected")
	ErrDeviceNotFound = errors.New("device not found")
)

// TokenPair is what a login or a refresh returns. The plain values are never retrievable again.
type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	DeviceID         uuid.UUID `json:"device_id"`
}

// DeviceInfo is the user-visible record of an installation.
type DeviceInfo struct {
	ID         uuid.UUID  `json:"id"`
	Label      string     `json:"label"`
	Platform   string     `json:"platform"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	Current    bool       `json:"current"`
}

// DeviceCredential is what a valid access token resolves to.
type DeviceCredential struct {
	UserID   uuid.UUID
	DeviceID uuid.UUID
	TokenKey string // digest of the access token (hex), the key SSE rechecks with
}

// DeviceStore is the persistence boundary of device sessions.
type DeviceStore interface {
	IssueForLogin(ctx context.Context, userID uuid.UUID, label, platform string, previousDeviceID *uuid.UUID) (TokenPair, error)
	Refresh(ctx context.Context, refreshToken string) (TokenPair, error)
	ResolveAccess(ctx context.Context, accessToken string) (DeviceCredential, error)
	StillValid(ctx context.Context, tokenKey string) error
	RevokeByAccess(ctx context.Context, accessToken, reason string) error
	RevokeByRefresh(ctx context.Context, refreshToken, reason string) error
	RevokeDevice(ctx context.Context, userID, deviceID uuid.UUID, reason string) error
	ListDevices(ctx context.Context, userID uuid.UUID, currentDeviceID uuid.UUID) ([]DeviceInfo, error)
}

type PostgresDeviceStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresDeviceStore(pool *pgxpool.Pool) *PostgresDeviceStore {
	return &PostgresDeviceStore{pool: pool, now: time.Now}
}

func newToken(prefix string) (plain string, digest []byte, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", nil, err
	}
	plain = prefix + base64.RawURLEncoding.EncodeToString(b)
	return plain, tokenDigest(plain), nil
}

func tokenDigest(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

func digestHex(d []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(d)*2)
	for _, c := range d {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}

func hasPrefix(token, prefix string) bool {
	return strings.HasPrefix(token, prefix) && len(token) > len(prefix)
}

// IssueForLogin creates a new installation (device + family) for a user whose identity was already validated, and its first token pair.
// previousDeviceID (what the app remembers from an earlier login) is revoked ONLY when it belongs to the same user.
func (s *PostgresDeviceStore) IssueForLogin(ctx context.Context, userID uuid.UUID, label, platform string, previousDeviceID *uuid.UUID) (TokenPair, error) {
	label = strings.TrimSpace(label)
	if len(label) > 80 {
		label = label[:80]
	}
	switch platform {
	case "android", "ios":
	default:
		platform = "other"
	}
	now := s.now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TokenPair{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Logins of one user are serialised (a single advisory lock, released at commit): two concurrent logins that lock several families in
	// different orders could otherwise deadlock. Refresh and revocation lock ONE family row each, so they cannot form a cycle with this.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('auth_devices:' || $1::text, 0))`, userID.String()); err != nil {
		return TokenPair{}, err
	}
	if previousDeviceID != nil {
		if err := revokeDeviceTx(ctx, tx, userID, *previousDeviceID, RevokedReplaced, now); err != nil && !errors.Is(err, ErrDeviceNotFound) {
			return TokenPair{}, err
		}
	}
	// Bounded number of live installations per user: the least recently used ones are revoked.
	if _, err := tx.Exec(ctx, `
		UPDATE auth_families SET revoked_at = $2, revoked_reason = $3
		WHERE revoked_at IS NULL AND device_id IN (
			SELECT id FROM auth_devices WHERE user_id = $1 AND revoked_at IS NULL
			ORDER BY COALESCE(last_seen_at, created_at) DESC OFFSET $4)`, userID, now, RevokedLimit, maxActiveDevices-1); err != nil {
		return TokenPair{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_devices SET revoked_at = $2, revoked_reason = $3
		WHERE revoked_at IS NULL AND id IN (
			SELECT id FROM auth_devices WHERE user_id = $1 AND revoked_at IS NULL
			ORDER BY COALESCE(last_seen_at, created_at) DESC OFFSET $4)`, userID, now, RevokedLimit, maxActiveDevices-1); err != nil {
		return TokenPair{}, err
	}

	var deviceID, familyID uuid.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO auth_devices(user_id, label, platform, created_at, last_seen_at) VALUES ($1,$2,$3,$4,$4) RETURNING id`,
		userID, label, platform, now).Scan(&deviceID); err != nil {
		return TokenPair{}, err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO auth_families(device_id, user_id, created_at, absolute_expires_at) VALUES ($1,$2,$3,$4) RETURNING id`,
		deviceID, userID, now, now.Add(FamilyAbsoluteTTL)).Scan(&familyID); err != nil {
		return TokenPair{}, err
	}
	pair, err := insertPair(ctx, tx, familyID, userID, deviceID, nil, now, now.Add(FamilyAbsoluteTTL))
	if err != nil {
		return TokenPair{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenPair{}, err
	}
	// Housekeeping after the commit (a failure here must never fail a login): expired access tokens and installations revoked or
	// past their absolute limit for more than 30 days.
	_, _ = s.pool.Exec(ctx, `DELETE FROM auth_access_tokens WHERE expires_at < $1`, now)
	_, _ = s.pool.Exec(ctx, `DELETE FROM auth_devices d WHERE d.user_id = $1 AND (d.revoked_at < $2 OR EXISTS (
		SELECT 1 FROM auth_families f WHERE f.device_id = d.id AND f.absolute_expires_at < $2))`, userID, now.Add(-30*24*time.Hour))
	return pair, nil
}

// insertPair writes a fresh access token and refresh token for the family. Any earlier access token of the family is dropped, so only
// the newest one is valid.
func insertPair(ctx context.Context, tx pgx.Tx, familyID, userID, deviceID uuid.UUID, parentHash []byte, now, absoluteExpiry time.Time) (TokenPair, error) {
	access, accessDigest, err := newToken(AccessTokenPrefix)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, refreshDigest, err := newToken(RefreshTokenPrefix)
	if err != nil {
		return TokenPair{}, err
	}
	accessExp := now.Add(AccessTokenTTL)
	refreshExp := now.Add(RefreshTokenTTL)
	if refreshExp.After(absoluteExpiry) {
		refreshExp = absoluteExpiry
	}
	if accessExp.After(absoluteExpiry) {
		accessExp = absoluteExpiry
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_access_tokens WHERE family_id = $1`, familyID); err != nil {
		return TokenPair{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_access_tokens(token_hash, family_id, user_id, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		accessDigest, familyID, userID, now, accessExp); err != nil {
		return TokenPair{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_refresh_tokens(token_hash, family_id, parent_hash, created_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		refreshDigest, familyID, parentHash, now, refreshExp); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: refresh, RefreshExpiresAt: refreshExp, DeviceID: deviceID}, nil
}

// Refresh rotates a refresh token. The family row is locked FIRST (the same lock revocation takes), then the token is re-read under it.
// A spent token revokes the whole installation; the revocation is committed before ErrRefreshReuse is returned.
func (s *PostgresDeviceStore) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	if !hasPrefix(refreshToken, RefreshTokenPrefix) {
		return TokenPair{}, ErrInvalidToken
	}
	digest := tokenDigest(refreshToken)
	now := s.now()

	var familyID uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT family_id FROM auth_refresh_tokens WHERE token_hash = $1`, digest).Scan(&familyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenPair{}, ErrInvalidToken
		}
		return TokenPair{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TokenPair{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		userID, deviceID uuid.UUID
		absolute         time.Time
		familyRevoked    *time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT user_id, device_id, absolute_expires_at, revoked_at FROM auth_families WHERE id = $1 FOR UPDATE`, familyID).
		Scan(&userID, &deviceID, &absolute, &familyRevoked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenPair{}, ErrInvalidToken
		}
		return TokenPair{}, err
	}
	var (
		usedAt  *time.Time
		expires time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT used_at, expires_at FROM auth_refresh_tokens WHERE token_hash = $1 FOR UPDATE`, digest).Scan(&usedAt, &expires); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TokenPair{}, ErrInvalidToken
		}
		return TokenPair{}, err
	}
	if familyRevoked != nil {
		return TokenPair{}, ErrInvalidToken
	}
	if usedAt != nil {
		if err := revokeFamilyLocked(ctx, tx, familyID, deviceID, RevokedReuse, now); err != nil {
			return TokenPair{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return TokenPair{}, err
		}
		return TokenPair{}, ErrRefreshReuse
	}
	if !expires.After(now) || !absolute.After(now) {
		return TokenPair{}, ErrInvalidToken
	}
	var deviceRevoked *time.Time
	if err := tx.QueryRow(ctx, `SELECT revoked_at FROM auth_devices WHERE id = $1`, deviceID).Scan(&deviceRevoked); err != nil || deviceRevoked != nil {
		return TokenPair{}, ErrInvalidToken
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_refresh_tokens SET used_at = $2 WHERE token_hash = $1`, digest, now); err != nil {
		return TokenPair{}, err
	}
	pair, err := insertPair(ctx, tx, familyID, userID, deviceID, digest, now, absolute)
	if err != nil {
		return TokenPair{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_devices SET last_seen_at = $2 WHERE id = $1`, deviceID, now); err != nil {
		return TokenPair{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

// revokeFamilyLocked revokes the family and its installation. The caller already holds (or takes here) the family row lock.
func revokeFamilyLocked(ctx context.Context, tx pgx.Tx, familyID, deviceID uuid.UUID, reason string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE auth_families SET revoked_at = COALESCE(revoked_at, $2), revoked_reason = COALESCE(revoked_reason, $3) WHERE id = $1`, familyID, now, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_devices SET revoked_at = COALESCE(revoked_at, $2), revoked_reason = COALESCE(revoked_reason, $3) WHERE id = $1`, deviceID, now, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM auth_access_tokens WHERE family_id = $1`, familyID)
	return err
}

// revokeDeviceTx revokes one installation of the given user (ErrDeviceNotFound when it is not theirs). The family lock is taken first.
func revokeDeviceTx(ctx context.Context, tx pgx.Tx, userID, deviceID uuid.UUID, reason string, now time.Time) error {
	var familyID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT f.id FROM auth_families f JOIN auth_devices d ON d.id = f.device_id
		WHERE d.id = $1 AND d.user_id = $2 FOR UPDATE OF f`, deviceID, userID).Scan(&familyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeviceNotFound
	}
	if err != nil {
		return err
	}
	return revokeFamilyLocked(ctx, tx, familyID, deviceID, reason, now)
}

func (s *PostgresDeviceStore) RevokeDevice(ctx context.Context, userID, deviceID uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := revokeDeviceTx(ctx, tx, userID, deviceID, reason, s.now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeByAccess ends the installation that owns a valid access token (logout). An unknown token is a no-op for the caller.
func (s *PostgresDeviceStore) RevokeByAccess(ctx context.Context, accessToken, reason string) error {
	if !hasPrefix(accessToken, AccessTokenPrefix) {
		return ErrInvalidToken
	}
	cred, err := s.ResolveAccess(ctx, accessToken)
	if err != nil {
		return err
	}
	return s.RevokeDevice(ctx, cred.UserID, cred.DeviceID, reason)
}

// RevokeByRefresh ends the installation that owns a refresh token (logout when the access token already expired). Possession is the proof.
func (s *PostgresDeviceStore) RevokeByRefresh(ctx context.Context, refreshToken, reason string) error {
	if !hasPrefix(refreshToken, RefreshTokenPrefix) {
		return ErrInvalidToken
	}
	var deviceID, userID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT f.device_id, f.user_id FROM auth_refresh_tokens r JOIN auth_families f ON f.id = r.family_id WHERE r.token_hash = $1`,
		tokenDigest(refreshToken)).Scan(&deviceID, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidToken
	}
	if err != nil {
		return err
	}
	return s.RevokeDevice(ctx, userID, deviceID, reason)
}

const validAccessSQL = `
	SELECT a.user_id, f.device_id FROM auth_access_tokens a
	JOIN auth_families f ON f.id = a.family_id
	JOIN auth_devices d ON d.id = f.device_id
	WHERE a.token_hash = $1 AND a.expires_at > $2 AND f.revoked_at IS NULL AND f.absolute_expires_at > $2 AND d.revoked_at IS NULL`

// ResolveAccess maps an access token to its user and installation. Everything is checked on every call: revocation is immediate.
func (s *PostgresDeviceStore) ResolveAccess(ctx context.Context, accessToken string) (DeviceCredential, error) {
	if !hasPrefix(accessToken, AccessTokenPrefix) {
		return DeviceCredential{}, ErrInvalidToken
	}
	digest := tokenDigest(accessToken)
	now := s.now()
	var cred DeviceCredential
	if err := s.pool.QueryRow(ctx, validAccessSQL, digest, now).Scan(&cred.UserID, &cred.DeviceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeviceCredential{}, ErrInvalidToken
		}
		return DeviceCredential{}, err
	}
	cred.TokenKey = digestHex(digest)
	// Throttled bookkeeping: at most one write per installation per minute, never on the request's critical error path.
	_, _ = s.pool.Exec(ctx, `UPDATE auth_devices SET last_seen_at = $2 WHERE id = $1 AND (last_seen_at IS NULL OR last_seen_at < $3)`,
		cred.DeviceID, now, now.Add(-deviceLastSeenSlack))
	return cred, nil
}

// StillValid is the cheap re-check a long-lived stream (SSE) runs: the access token it was opened with must still be valid and unrevoked.
func (s *PostgresDeviceStore) StillValid(ctx context.Context, tokenKey string) error {
	digest, err := hexDigest(tokenKey)
	if err != nil {
		return ErrInvalidToken
	}
	var userID, deviceID uuid.UUID
	if err := s.pool.QueryRow(ctx, validAccessSQL, digest, s.now()).Scan(&userID, &deviceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		return err
	}
	return nil
}

func hexDigest(h string) ([]byte, error) {
	if len(h) != 64 {
		return nil, ErrInvalidToken
	}
	out := make([]byte, 32)
	for i := 0; i < 32; i++ {
		hi, ok1 := unhex(h[2*i])
		lo, ok2 := unhex(h[2*i+1])
		if !ok1 || !ok2 {
			return nil, ErrInvalidToken
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

func (s *PostgresDeviceStore) ListDevices(ctx context.Context, userID uuid.UUID, currentDeviceID uuid.UUID) ([]DeviceInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, label, platform, created_at, last_seen_at FROM auth_devices
		WHERE user_id = $1 AND revoked_at IS NULL ORDER BY COALESCE(last_seen_at, created_at) DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceInfo{}
	for rows.Next() {
		var d DeviceInfo
		if err := rows.Scan(&d.ID, &d.Label, &d.Platform, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		d.Current = d.ID == currentDeviceID
		out = append(out, d)
	}
	return out, rows.Err()
}
