package connectors

import (
	"context"
	"fmt"
)

// Connector — interface para conectores de APIs externas
type Connector interface {
	// Name — nome do conector (slack, hubspot, zapier, etc)
	Name() string

	// Authenticate — valida credenciais
	Authenticate(ctx context.Context, credentials map[string]interface{}) error

	// Execute — executa ação no serviço externo
	Execute(ctx context.Context, action string, params map[string]interface{}) (map[string]interface{}, error)

	// GetActions — lista ações disponíveis
	GetActions() []ActionSpec
}

// ActionSpec — especificação de uma ação disponível
type ActionSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Params      map[string]ParamSpec   `json:"params"`
	Returns     map[string]interface{} `json:"returns"`
}

// ParamSpec — especificação de um parâmetro
type ParamSpec struct {
	Type        string      `json:"type"`        // string, number, boolean, array, object
	Description string      `json:"description"`
	Required    bool        `json:"required"`
	Example     interface{} `json:"example,omitempty"`
}

// Registry — registro de conectores disponíveis
type Registry struct {
	connectors map[string]Connector
}

// NewRegistry — cria novo registry
func NewRegistry() *Registry {
	return &Registry{
		connectors: make(map[string]Connector),
	}
}

// Register — registra novo conector
func (r *Registry) Register(connector Connector) error {
	name := connector.Name()
	if _, exists := r.connectors[name]; exists {
		return fmt.Errorf("connector already registered: %s", name)
	}
	r.connectors[name] = connector
	return nil
}

// Get — obtém conector por nome
func (r *Registry) Get(name string) (Connector, error) {
	connector, ok := r.connectors[name]
	if !ok {
		return nil, fmt.Errorf("connector not found: %s", name)
	}
	return connector, nil
}

// List — lista todos os conectores
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.connectors))
	for name := range r.connectors {
		names = append(names, name)
	}
	return names
}
