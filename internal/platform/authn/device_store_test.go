package authn

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

func deviceStoreForTest(t *testing.T) (*PostgresDeviceStore, *pgxpool.Pool, func() uuid.UUID) {
	t.Helper()
	seedURL, _ := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	newUser := func() uuid.UUID {
		id := uuid.New()
		if _, err := pool.Exec(context.Background(), `INSERT INTO users(id, external_subject, email, status) VALUES ($1,$2,$3,'active')`, id, id.String(), id.String()+"@test.local"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id) })
		return id
	}
	return NewPostgresDeviceStore(pool), pool, newUser
}

func TestDeviceLoginAccessAndLogout(t *testing.T) {
	store, pool, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	pair, err := store.IssueForLogin(ctx, user, "Pixel", "android", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPrefix(pair.AccessToken, AccessTokenPrefix) || !hasPrefix(pair.RefreshToken, RefreshTokenPrefix) {
		t.Fatalf("token shapes: %q %q", pair.AccessToken, pair.RefreshToken)
	}
	cred, err := store.ResolveAccess(ctx, pair.AccessToken)
	if err != nil || cred.UserID != user || cred.DeviceID != pair.DeviceID {
		t.Fatalf("resolve: %+v %v", cred, err)
	}
	if err := store.StillValid(ctx, cred.TokenKey); err != nil {
		t.Fatalf("StillValid: %v", err)
	}
	// Only digests are stored: the plain values must not be anywhere in the tables.
	var leaked int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM auth_access_tokens WHERE encode(token_hash,'escape') LIKE '%omn_at_%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("plain access token stored")
	}
	devices, err := store.ListDevices(ctx, user, pair.DeviceID)
	if err != nil || len(devices) != 1 || !devices[0].Current || devices[0].Label != "Pixel" || devices[0].Platform != "android" {
		t.Fatalf("list: %+v %v", devices, err)
	}
	if err := store.RevokeByAccess(ctx, pair.AccessToken, RevokedLogout); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAccess(ctx, pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("access after logout: %v", err)
	}
	if err := store.StillValid(ctx, cred.TokenKey); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("StillValid after logout: %v", err)
	}
	if _, err := store.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh after logout: %v", err)
	}
}

func TestRefreshRotatesAndReuseRevokesTheInstallation(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	first, err := store.IssueForLogin(ctx, user, "", "ios", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.DeviceID != first.DeviceID || second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("rotation must keep the installation and change both tokens")
	}
	if _, err := store.ResolveAccess(ctx, first.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the previous access token must stop working after a rotation: %v", err)
	}
	if _, err := store.ResolveAccess(ctx, second.AccessToken); err != nil {
		t.Fatal(err)
	}
	// Replaying the spent refresh token is a theft signal: everything of the installation dies.
	if _, err := store.Refresh(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshReuse) {
		t.Fatalf("reuse: %v", err)
	}
	if _, err := store.ResolveAccess(ctx, second.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("access must be dead after reuse: %v", err)
	}
	if _, err := store.Refresh(ctx, second.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the successor refresh must be dead after reuse: %v", err)
	}
	if devices, _ := store.ListDevices(ctx, user, uuid.Nil); len(devices) != 0 {
		t.Fatalf("revoked installation still listed: %+v", devices)
	}
}

func TestRefreshHonoursSlidingAndAbsoluteLimits(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	base := time.Now()
	store.now = func() time.Time { return base }
	pair, err := store.IssueForLogin(ctx, user, "", "android", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Sliding: a refresh token unused for more than 30 days is dead.
	store.now = func() time.Time { return base.Add(RefreshTokenTTL + time.Hour) }
	if _, err := store.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired refresh accepted: %v", err)
	}

	// Absolute: rotating every 29 days can never pass the 90-day limit set at login.
	pair, err = store.IssueForLogin(ctx, user, "", "android", nil)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return base }
	pair, err = store.IssueForLogin(ctx, user, "", "android", nil)
	if err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	for i := 1; i <= 3; i++ {
		at := base.Add(time.Duration(29*i) * day)
		store.now = func() time.Time { return at }
		if pair, err = store.Refresh(ctx, pair.RefreshToken); err != nil {
			t.Fatalf("rotation %d at day %d: %v", i, 29*i, err)
		}
		if pair.RefreshExpiresAt.After(base.Add(FamilyAbsoluteTTL)) {
			t.Fatalf("refresh expiry %v passes the absolute limit %v", pair.RefreshExpiresAt, base.Add(FamilyAbsoluteTTL))
		}
	}
	store.now = func() time.Time { return base.Add(91 * day) }
	if _, err := store.Refresh(ctx, pair.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh past the 90-day absolute limit accepted: %v", err)
	}
	if _, err := store.ResolveAccess(ctx, pair.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("access past the absolute limit accepted: %v", err)
	}
}

func TestConcurrentRefreshWithTheSameTokenYieldsExactlyOneSuccessor(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	pair, err := store.IssueForLogin(ctx, user, "", "android", nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 12
	var ok, reuse, other int32
	pairs := make(chan TokenPair, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, err := store.Refresh(ctx, pair.RefreshToken)
			switch {
			case err == nil:
				atomic.AddInt32(&ok, 1)
				pairs <- p
			case errors.Is(err, ErrRefreshReuse):
				atomic.AddInt32(&reuse, 1)
			default:
				atomic.AddInt32(&other, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(pairs)
	if ok != 1 {
		t.Fatalf("exactly one refresh may win, got ok=%d reuse=%d other=%d", ok, reuse, other)
	}
	// The losers saw the spent token: the whole installation is revoked, the single winner's tokens included.
	for p := range pairs {
		if _, err := store.ResolveAccess(ctx, p.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("the winner's access token survived the reuse revocation: %v", err)
		}
	}
}

func TestRevocationRacingWithRefreshNeverLeavesALiveSuccessor(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	for round := 0; round < 25; round++ {
		pair, err := store.IssueForLogin(ctx, user, "", "android", nil)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var next TokenPair
		var refreshErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			next, refreshErr = store.Refresh(ctx, pair.RefreshToken)
		}()
		go func() {
			defer wg.Done()
			<-start
			if err := store.RevokeDevice(ctx, user, pair.DeviceID, RevokedUser); err != nil {
				t.Errorf("revoke: %v", err)
			}
		}()
		close(start)
		wg.Wait()
		// Whatever the interleaving, once the revocation returned nothing of the installation may work.
		if refreshErr == nil {
			if _, err := store.ResolveAccess(ctx, next.AccessToken); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("round %d: access token alive after revocation: %v", round, err)
			}
			if _, err := store.Refresh(ctx, next.RefreshToken); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("round %d: successor refresh alive after revocation: %v", round, err)
			}
		}
	}
}

func TestDeviceRevocationIsPerUserAndPreviousDeviceIsNotTrusted(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	alice, mallory := newUser(), newUser()
	a, err := store.IssueForLogin(ctx, alice, "Alice phone", "android", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Another user cannot revoke Alice's installation, nor learn that it exists.
	if err := store.RevokeDevice(ctx, mallory, a.DeviceID, RevokedUser); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("foreign revoke: %v", err)
	}
	// A previous_device_id of somebody else is ignored (never trusted).
	if _, err := store.IssueForLogin(ctx, mallory, "m", "ios", &a.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAccess(ctx, a.AccessToken); err != nil {
		t.Fatalf("Alice was logged out by Mallory's login: %v", err)
	}
	// The user's own previous installation is replaced.
	b, err := store.IssueForLogin(ctx, alice, "Alice phone again", "android", &a.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveAccess(ctx, a.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the replaced installation must stop working: %v", err)
	}
	if _, err := store.ResolveAccess(ctx, b.AccessToken); err != nil {
		t.Fatal(err)
	}
}

func TestDevicesPerUserAreBounded(t *testing.T) {
	store, _, newUser := deviceStoreForTest(t)
	ctx := context.Background()
	user := newUser()
	var first TokenPair
	for i := 0; i < maxActiveDevices+3; i++ {
		p, err := store.IssueForLogin(ctx, user, "", "android", nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = p
		}
	}
	devices, _ := store.ListDevices(ctx, user, uuid.Nil)
	if len(devices) != maxActiveDevices {
		t.Fatalf("live installations = %d, want %d", len(devices), maxActiveDevices)
	}
	if _, err := store.ResolveAccess(ctx, first.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("the oldest installation must have been revoked: %v", err)
	}
}

func TestMalformedAndForeignTokensAreRejected(t *testing.T) {
	store, _, _ := deviceStoreForTest(t)
	ctx := context.Background()
	for _, tok := range []string{"", "omn_at_", "omn_at_nope", "omn_rt_nope", "eyJhbGciOi.J.W", AccessTokenPrefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := store.ResolveAccess(ctx, tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("ResolveAccess(%q) = %v", tok, err)
		}
		if _, err := store.Refresh(ctx, tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Refresh(%q) = %v", tok, err)
		}
	}
}
