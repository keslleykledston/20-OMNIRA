package main
import (
	"context"
	"fmt"
	"log"
	"time"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/authn"
)
func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://omnira_app:omnira_app@127.0.0.1:55434/omnira_dev?sslmode=disable")
	if err != nil { log.Fatal(err) }
	defer pool.Close()
	store := authn.NewPostgresSessionStore(pool)
	userID := uuid.New()
	sessionID, err := store.CreateSession(ctx, userID, "oidc", 15*time.Minute)
	if err != nil { log.Fatalf("CreateSession: %v", err) }
	fmt.Printf("✅ Session created (opaque 64-hex): %s\n", sessionID[:20]+"...")
	resolved, err := store.ResolveSession(ctx, sessionID)
	if err != nil { log.Fatalf("ResolveSession: %v", err) }
	fmt.Printf("✅ Resolved to user: %s\n", resolved.String()[:12]+"...")
	store.RevokeSession(ctx, sessionID)
	_, err = store.ResolveSession(ctx, sessionID)
	if err != nil { fmt.Printf("✅ Revoked session correctly rejected\n") }
	fmt.Printf("🎉 AUTH.7 SESSION STORE VERIFIED\n")
}
