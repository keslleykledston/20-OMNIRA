package connectors

import (
	"context"
	"strings"
)

// K3GCRMConnector adapta K3GCRMClient para a interface inboxapp.CRMConnector,
// retornando UUIDs como strings para desacoplamento do domain.
type K3GCRMConnector struct {
	client *K3GCRMClient
}

func NewK3GCRMConnector(client *K3GCRMClient) *K3GCRMConnector {
	return &K3GCRMConnector{client: client}
}

// FindCustomerByPhone procura contato no CRM por phone e companyID.
// Retorna a string do UUID ou string vazia se não encontrado.
func (c *K3GCRMConnector) FindCustomerByPhone(ctx context.Context, phone, companyID string) (string, error) {
	if c.client == nil {
		return "", ErrCRMNotConfigured
	}
	contact, err := c.client.FindCustomerByPhone(ctx, phone, companyID)
	if err != nil || contact == nil {
		return "", err
	}
	return strings.TrimSpace(contact.ID), nil
}

// CreateContact cria novo contato no CRM com name, phone e companyID.
// Retorna a string do UUID gerado ou erro.
func (c *K3GCRMConnector) CreateContact(ctx context.Context, name, phone, companyID string) (string, error) {
	if c.client == nil {
		return "", ErrCRMNotConfigured
	}
	contact, err := c.client.CreateContact(ctx, name, phone, companyID)
	if err != nil || contact == nil {
		return "", err
	}
	return strings.TrimSpace(contact.ID), nil
}
