package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Quota — configuração de rate limit
type Quota struct {
	MaxRequests int
	Window      time.Duration
}

// QuotaType — tipo de quota (tenant, user, global)
type QuotaType string

const (
	QuotaTypeTenant QuotaType = "tenant"
	QuotaTypeUser   QuotaType = "user"
	QuotaTypeGlobal QuotaType = "global"
)

// Token — representa uma requisição no sliding window
type Token struct {
	Timestamp time.Time
}

// Limiter — rate limiter com sliding window
type Limiter struct {
	mu     sync.RWMutex
	tokens map[string][]*Token // key: quotaType:id, value: tokens com timestamp
	quotas map[QuotaType]*Quota
}

// NewLimiter — cria novo rate limiter
func NewLimiter() *Limiter {
	return &Limiter{
		tokens: make(map[string][]*Token),
		quotas: map[QuotaType]*Quota{
			QuotaTypeTenant: {
				MaxRequests: 1000,
				Window:      time.Minute,
			},
			QuotaTypeUser: {
				MaxRequests: 100,
				Window:      time.Minute,
			},
			QuotaTypeGlobal: {
				MaxRequests: 10000,
				Window:      time.Minute,
			},
		},
	}
}

// SetQuota — define quota personalizada
func (l *Limiter) SetQuota(quotaType QuotaType, maxRequests int, window time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.quotas[quotaType] = &Quota{
		MaxRequests: maxRequests,
		Window:      window,
	}
}

// Allow — verifica se requisição é permitida
// Retorna (allowed, remaining, resetTime)
func (l *Limiter) Allow(ctx context.Context, quotaType QuotaType, id string) (bool, int, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := fmt.Sprintf("%s:%s", quotaType, id)
	quota := l.quotas[quotaType]
	if quota == nil {
		return true, -1, time.Time{} // No limit
	}

	now := time.Now()

	// Remover tokens expirados (fora da janela)
	cutoff := now.Add(-quota.Window)
	var validTokens []*Token
	for _, token := range l.tokens[key] {
		if token.Timestamp.After(cutoff) {
			validTokens = append(validTokens, token)
		}
	}

	// Contar requisições válidas
	requestCount := len(validTokens)

	if requestCount >= quota.MaxRequests {
		// Rate limit exceeded
		l.tokens[key] = validTokens
		resetTime := validTokens[0].Timestamp.Add(quota.Window)
		return false, 0, resetTime
	}

	// Adicionar novo token
	validTokens = append(validTokens, &Token{Timestamp: now})
	l.tokens[key] = validTokens

	remaining := quota.MaxRequests - len(validTokens)
	resetTime := now.Add(quota.Window)

	return true, remaining, resetTime
}

// GetStatus — retorna status atual do rate limit
func (l *Limiter) GetStatus(quotaType QuotaType, id string) (used int, limit int, resetTime time.Time) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	key := fmt.Sprintf("%s:%s", quotaType, id)
	quota := l.quotas[quotaType]
	if quota == nil {
		return 0, 0, time.Time{}
	}

	now := time.Now()
	cutoff := now.Add(-quota.Window)

	// Contar tokens válidos
	count := 0
	var oldestToken *Token
	for _, token := range l.tokens[key] {
		if token.Timestamp.After(cutoff) {
			count++
			if oldestToken == nil || token.Timestamp.Before(oldestToken.Timestamp) {
				oldestToken = token
			}
		}
	}

	resetTime = time.Time{}
	if oldestToken != nil {
		resetTime = oldestToken.Timestamp.Add(quota.Window)
	}

	return count, quota.MaxRequests, resetTime
}

// Reset — reseta limite para um id
func (l *Limiter) Reset(quotaType QuotaType, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := fmt.Sprintf("%s:%s", quotaType, id)
	delete(l.tokens, key)
}

// CleanupExpired — remove tokens expirados (pode ser chamado periodicamente)
func (l *Limiter) CleanupExpired() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	for key, tokens := range l.tokens {
		var validTokens []*Token

		for _, quota := range l.quotas {
			cutoff := now.Add(-quota.Window)
			for _, token := range tokens {
				if token.Timestamp.After(cutoff) {
					validTokens = append(validTokens, token)
				}
			}
		}

		if len(validTokens) == 0 {
			delete(l.tokens, key)
		} else {
			l.tokens[key] = validTokens
		}
	}
}
