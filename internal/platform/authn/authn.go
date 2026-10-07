package authn

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Principal — identidade autenticada (único ponto de entrada de authn).
type Principal struct {
	UserID  uuid.UUID
	Subject string
	// SessionKind/SessionKey identify the server-side credential this request authenticated with (cookie session or device access token),
	// so a long-lived stream can ask later whether it is still valid. Empty for a Bearer ID token (nothing server-side to revoke).
	SessionKind string
	SessionKey  string
	// DeviceID is set when the caller is a native app installation.
	DeviceID uuid.UUID
}

const (
	SessionKindCookie = "cookie"
	SessionKindDevice = "device"
)

// Authenticator — interface para estratégias de autenticação.
type Authenticator interface {
	// Verify retorna um Principal válido ou erro.
	Verify(context.Context, string) (*Principal, error)
}

const SessionCookieName = "omnira_session"

// JWTAuthenticator — suporta JWT assinado por RSA (OIDC-compatible).
type JWTAuthenticator struct {
	publicKey *rsa.PublicKey
	issuer    string
	audience  string
}

// Claims — JWT payload mínimo.
type Claims struct {
	Subject string `json:"sub"`
	UserID  string `json:"user_id,omitempty"`
	jwt.RegisteredClaims
}

// NewJWTAuthenticator — cria um authenticator JWT com chave pública RSA.
func NewJWTAuthenticator(publicKey *rsa.PublicKey, issuer, audience string) *JWTAuthenticator {
	return &JWTAuthenticator{
		publicKey: publicKey,
		issuer:    issuer,
		audience:  audience,
	}
}

// Verify — parse e valida o JWT.
func (a *JWTAuthenticator) Verify(_ context.Context, tokenString string) (*Principal, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validar alg
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return a.publicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}

	// Validar issuer e audience
	if claims.Issuer != a.issuer {
		return nil, fmt.Errorf("invalid issuer: %s (expected %s)", claims.Issuer, a.issuer)
	}

	if len(claims.Audience) == 0 {
		return nil, errors.New("missing audience in token")
	}

	found := false
	for _, aud := range claims.Audience {
		if aud == a.audience {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("invalid audience: expected %s", a.audience)
	}

	// Preferir o claim "user_id" explícito (emitido pelo login real/mock e que
	// bate com os UUIDs de membership persistidos). Só cair para um UUID
	// derivado do subject se o claim não vier — não deve acontecer em tokens
	// emitidos por este backend, mas evita panic em tokens externos/legados.
	userID, err := uuid.Parse(claims.UserID)
	if err != nil {
		userID = uuid.NewSHA1([16]byte{}, []byte(claims.Subject))
	}

	return &Principal{
		UserID:  userID,
		Subject: claims.Subject,
	}, nil
}

// ContextKey — chave para armazenar Principal no context.
type ContextKey string

const PrincipalKey ContextKey = "principal"

// WithPrincipal — armazena Principal no context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, PrincipalKey, p)
}

// FromContext — recupera Principal do context.
func FromContext(ctx context.Context) (*Principal, error) {
	principal, ok := ctx.Value(PrincipalKey).(*Principal)
	if !ok {
		return nil, errors.New("principal not found in context")
	}
	return principal, nil
}

// Middleware — HTTP middleware que valida token Bearer/session e injeta Principal.
// Suporta dois modos:
// - Bearer token (API clients, dev mock)
// - Session cookie (OIDC browsers)
func Middleware(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			var token string
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) != 2 || parts[0] != "Bearer" {
					http.Error(w, "invalid authorization header", http.StatusUnauthorized)
					return
				}
				token = parts[1]
			} else if cookie, err := r.Cookie(SessionCookieName); err == nil {
				token = cookie.Value
			}
			if token == "" {
				http.Error(w, "missing authentication", http.StatusUnauthorized)
				return
			}
			principal, err := auth.Verify(r.Context(), token)
			if err != nil {
				http.Error(w, fmt.Sprintf("authentication failed: %v", err), http.StatusUnauthorized)
				return
			}

			// Injetar Principal no context
			ctx := WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// SessionMiddleware — validates session cookie and injects Principal (OIDC only).
func SessionMiddleware(store SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				http.Error(w, "missing session", http.StatusUnauthorized)
				return
			}

			userID, err := store.ResolveSession(r.Context(), cookie.Value)
			if err != nil {
				http.Error(w, "invalid or expired session", http.StatusUnauthorized)
				return
			}

			principal := &Principal{UserID: userID, Subject: userID.String()}
			ctx := WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WebMiddleware is the boundary for browser-facing routes. It supports two
// authenticated callers with different trust models, never blending them:
//   - Bearer header: a real JWT/OIDC-token API consumer (dev tooling, API
//     clients). Verified cryptographically via auth, exactly like Middleware.
//   - omnira_session cookie: the browser. The cookie is always an opaque
//     session ID minted by CreateSession; it is resolved server-side via
//     store.ResolveSession, never parsed as a token. A JWT accidentally
//     placed in this cookie (e.g. an old client, or a downgrade attempt)
//     is looked up in auth_sessions, fails to match any row, and is
//     rejected — there is no fallback from opaque-lookup to JWT-parse for
//     the cookie path, which is what eliminates the legacy ambiguity.
func WebMiddleware(auth Authenticator, store SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authHeader := r.Header.Get("Authorization"); authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) != 2 || parts[0] != "Bearer" {
					http.Error(w, "invalid authorization header", http.StatusUnauthorized)
					return
				}
				principal, err := auth.Verify(r.Context(), parts[1])
				if err != nil {
					http.Error(w, fmt.Sprintf("authentication failed: %v", err), http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
				return
			}

			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				http.Error(w, "missing authentication", http.StatusUnauthorized)
				return
			}
			if store == nil {
				http.Error(w, "session store not configured", http.StatusInternalServerError)
				return
			}
			userID, err := store.ResolveSession(r.Context(), cookie.Value)
			if err != nil {
				http.Error(w, "invalid or expired session", http.StatusUnauthorized)
				return
			}
			principal := &Principal{UserID: userID, Subject: userID.String(), SessionKind: SessionKindCookie, SessionKey: cookie.Value}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
		})
	}
}
