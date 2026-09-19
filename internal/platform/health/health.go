package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
)

// Status — status de saúde de um componente.
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusUnhealthy Status = "unhealthy"
	StatusUnknown   Status = "unknown"
)

// ComponentHealth — saúde de um componente individual.
type ComponentHealth struct {
	Name    string        `json:"name"`
	Status  Status        `json:"status"`
	Message string        `json:"message,omitempty"`
	Latency time.Duration `json:"latency_ms"`
}

// HealthCheck — valida saúde dos componentes.
type HealthCheck struct {
	mu         sync.RWMutex
	dbPool     *pgxpool.Pool
	natsConn   *nats.Conn
	components map[string]*ComponentHealth
}

// NewHealthCheck — cria um novo HealthCheck.
func NewHealthCheck(dbPool *pgxpool.Pool, natsConn *nats.Conn) *HealthCheck {
	return &HealthCheck{
		dbPool:     dbPool,
		natsConn:   natsConn,
		components: make(map[string]*ComponentHealth),
	}
}

// Check — valida saúde de todos os componentes.
func (h *HealthCheck) Check(ctx context.Context) map[string]*ComponentHealth {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.components = make(map[string]*ComponentHealth)

	// Database
	h.checkDatabase(ctx)

	// NATS
	h.checkNATS(ctx)

	return h.components
}

// checkDatabase — valida conexão com PostgreSQL.
func (h *HealthCheck) checkDatabase(ctx context.Context) {
	start := time.Now()
	name := "database"

	if h.dbPool == nil {
		h.components[name] = &ComponentHealth{
			Name:    name,
			Status:  StatusUnknown,
			Message: "database pool not configured",
		}
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := h.dbPool.Ping(ctx)
	latency := time.Since(start)

	if err != nil {
		h.components[name] = &ComponentHealth{
			Name:    name,
			Status:  StatusUnhealthy,
			Message: fmt.Sprintf("ping failed: %v", err),
			Latency: latency,
		}
		return
	}

	h.components[name] = &ComponentHealth{
		Name:    name,
		Status:  StatusHealthy,
		Latency: latency,
	}
}

// checkNATS — valida conexão com NATS.
func (h *HealthCheck) checkNATS(ctx context.Context) {
	start := time.Now()
	name := "nats"

	if h.natsConn == nil {
		h.components[name] = &ComponentHealth{
			Name:    name,
			Status:  StatusUnknown,
			Message: "nats connection not configured",
		}
		return
	}

	// Check connection status
	if !h.natsConn.IsConnected() {
		h.components[name] = &ComponentHealth{
			Name:    name,
			Status:  StatusUnhealthy,
			Message: "not connected",
			Latency: time.Since(start),
		}
		return
	}

	latency := time.Since(start)

	h.components[name] = &ComponentHealth{
		Name:    name,
		Status:  StatusHealthy,
		Latency: latency,
	}
}

// IsHealthy — retorna true se todos os componentes estão healthy.
func (h *HealthCheck) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, component := range h.components {
		if component.Status != StatusHealthy {
			return false
		}
	}

	return true
}
