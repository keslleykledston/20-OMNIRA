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
}

// Authenticator — interface para estratégias de autenticação.
type Authenticator interface {
	// Verify retorna um Principal válido ou erro.
	Verify(tokenString string) (*Principal, error)
}

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
func (a *JWTAuthenticator) Verify(tokenString string) (*Principal, error) {
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

// Middleware — HTTP middleware que valida token Bearer e injeta Principal.
func Middleware(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extrair token do header Authorization
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				http.Error(w, "invalid authorization header", http.StatusUnauthorized)
				return
			}

			token := parts[1]
			principal, err := auth.Verify(token)
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
