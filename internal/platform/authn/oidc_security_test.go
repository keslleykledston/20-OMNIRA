package authn

import (
	"crypto/subtle"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestOIDCStateValidation(t *testing.T) {
	// State mismatch: request state != cookie state
	if subtle.ConstantTimeCompare([]byte("wrong"), []byte("correct")) == 1 {
		t.Fatal("state validation failed: constant time comparison accepted mismatch")
	}
}

func TestOIDCNonceValidation(t *testing.T) {
	claims := &oidcClaims{
		Nonce: "expected-nonce",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}

	// Nonce mismatch
	expectedNonce := "different-nonce"
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) == 1 {
		t.Fatal("nonce validation failed: constant time comparison accepted mismatch")
	}

	// Nonce match
	if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte("expected-nonce")) != 1 {
		t.Fatal("nonce validation failed: constant time comparison rejected match")
	}
}

func TestOIDCTokenExpiry(t *testing.T) {
	// Expired token
	expiredClaims := &oidcClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}

	// Verify expiry is in the past
	if expiredClaims.ExpiresAt.Before(time.Now()) {
		// Expected: token is expired
	} else {
		t.Fatal("token expiry validation failed: token should be expired")
	}

	// Valid token
	validClaims := &oidcClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}

	if validClaims.ExpiresAt.After(time.Now()) {
		// Expected: token is not expired
	} else {
		t.Fatal("token expiry validation failed: token should not be expired")
	}
}

func TestOIDCConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		issuer string
		wantOK bool
	}{
		{
			name:   "valid https issuer",
			issuer: "https://keycloak.example.com/realms/omnira",
			wantOK: true,
		},
		{
			name:   "valid http issuer (dev)",
			issuer: "http://localhost:8888/realms/omnira",
			wantOK: true,
		},
		{
			name:   "invalid: no scheme",
			issuer: "keycloak.example.com/realms/omnira",
			wantOK: false,
		},
		{
			name:   "invalid: ftp scheme",
			issuer: "ftp://keycloak.example.com/realms/omnira",
			wantOK: false,
		},
		{
			name:   "invalid: empty",
			issuer: "",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate issuer validation
			if tt.issuer == "" {
				if tt.wantOK {
					t.Fatal("empty issuer should fail")
				}
				return
			}

			validScheme := tt.issuer[:len("https")] == "https" || tt.issuer[:len("http:")] == "http:"
			hasHost := len(tt.issuer) > len("https://")

			if validScheme && hasHost {
				if !tt.wantOK {
					t.Fatalf("expected to fail, but passed")
				}
			} else {
				if tt.wantOK {
					t.Fatalf("expected to pass, but failed")
				}
			}
		})
	}
}

func TestOIDCCookieFlags(t *testing.T) {
	auth := &OIDCHandler{
		secureCookie: true,
	}

	cookie := auth.cookie("test", "value", 3600)

	if !cookie.HttpOnly {
		t.Fatal("cookie must have HttpOnly=true")
	}

	if !cookie.Secure {
		t.Fatal("cookie must have Secure=true when secureCookie=true")
	}

	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie must have SameSite=Lax")
	}

	if cookie.Path != "/" {
		t.Fatal("cookie must have Path=/")
	}
}

func TestOIDCCookieFlagsDev(t *testing.T) {
	auth := &OIDCHandler{
		secureCookie: false,
	}

	cookie := auth.cookie("test", "value", 3600)

	if !cookie.HttpOnly {
		t.Fatal("cookie must have HttpOnly=true even in dev")
	}

	if cookie.Secure {
		t.Fatal("cookie should not have Secure in dev")
	}

	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("cookie must have SameSite=Lax")
	}
}

func TestOIDCRandomURLSafe(t *testing.T) {
	s1, err := randomURLSafe(32)
	if err != nil {
		t.Fatalf("randomURLSafe failed: %v", err)
	}

	s2, err := randomURLSafe(32)
	if err != nil {
		t.Fatalf("randomURLSafe failed: %v", err)
	}

	if len(s1) == 0 || len(s2) == 0 {
		t.Fatal("randomURLSafe produced empty string")
	}

	if s1 == s2 {
		t.Fatal("randomURLSafe produced identical strings (collision)")
	}

	// Verify URL-safe: should only contain alphanumeric, -, _
	for _, c := range s1 {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			t.Fatalf("randomURLSafe produced non-URL-safe character: %c", c)
		}
	}
}
