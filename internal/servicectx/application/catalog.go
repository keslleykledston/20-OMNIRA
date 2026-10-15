package application

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// PermManageCatalog is the existing company-administration key: the catalogs are the company's configuration.
const PermManageCatalog = "tenant.manage"

// Catalog serves the company's categories and result codes. Reading needs service.read; changing needs tenant.manage.
type Catalog struct {
	repo  ports.CatalogRepository
	authz ports.Authorizer
	svc   *Service
}

func NewCatalog(repo ports.CatalogRepository, authz ports.Authorizer, svc *Service) *Catalog {
	return &Catalog{repo: repo, authz: authz, svc: svc}
}

func (c *Catalog) check(ctx context.Context, perm string) error {
	w, err := c.svc.who(ctx)
	if err != nil {
		return err
	}
	return c.svc.allow(ctx, w, perm)
}

func label(in string) (string, error) {
	out, err := domain.CleanText(in, 80, "label")
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(out) == 0 || strings.ContainsAny(out, "\n\t") {
		return "", domain.Invalid("label is required and has no line breaks")
	}
	return out, nil
}

func (c *Catalog) Categories(ctx context.Context, includeInactive bool) ([]ports.Category, error) {
	if err := c.check(ctx, PermRead); err != nil {
		return nil, err
	}
	return c.repo.ListCategories(ctx, includeInactive)
}

func (c *Catalog) CreateCategory(ctx context.Context, code, lbl string) (*ports.Category, error) {
	if err := c.check(ctx, PermManageCatalog); err != nil {
		return nil, err
	}
	if !domain.IsToken(code) {
		return nil, domain.Invalid("code must be lower case letters, digits . _ -, up to 40 characters")
	}
	l, err := label(lbl)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	cat := &ports.Category{ID: uuid.New(), Code: code, Label: l, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := c.repo.CreateCategory(ctx, cat); err != nil {
		return nil, err
	}
	return cat, nil
}

func (c *Catalog) UpdateCategory(ctx context.Context, id uuid.UUID, lbl *string, active *bool) (*ports.Category, error) {
	if err := c.check(ctx, PermManageCatalog); err != nil {
		return nil, err
	}
	if lbl != nil {
		l, err := label(*lbl)
		if err != nil {
			return nil, err
		}
		lbl = &l
	}
	if lbl == nil && active == nil {
		return nil, domain.Invalid("nothing to change")
	}
	return c.repo.UpdateCategory(ctx, id, lbl, active)
}

func (c *Catalog) ResultCodes(ctx context.Context, includeInactive bool) ([]ports.ResultCode, error) {
	if err := c.check(ctx, PermRead); err != nil {
		return nil, err
	}
	return c.repo.ListResultCodes(ctx, includeInactive)
}

func (c *Catalog) CreateResultCode(ctx context.Context, code, canonical, lbl string) (*ports.ResultCode, error) {
	if err := c.check(ctx, PermManageCatalog); err != nil {
		return nil, err
	}
	if !domain.IsToken(code) || domain.IsCanonicalResolution(code) {
		return nil, domain.Invalid("code must be a lower case token that does not take a built-in name")
	}
	if !domain.IsCanonicalResolution(canonical) {
		return nil, domain.Invalid("canonical_code must be one of the built-in results")
	}
	l, err := label(lbl)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rc := &ports.ResultCode{ID: uuid.New(), Code: code, CanonicalCode: canonical, Label: l, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := c.repo.CreateResultCode(ctx, rc); err != nil {
		return nil, err
	}
	return rc, nil
}

func (c *Catalog) UpdateResultCode(ctx context.Context, id uuid.UUID, lbl *string, active *bool) (*ports.ResultCode, error) {
	if err := c.check(ctx, PermManageCatalog); err != nil {
		return nil, err
	}
	if lbl != nil {
		l, err := label(*lbl)
		if err != nil {
			return nil, err
		}
		lbl = &l
	}
	if lbl == nil && active == nil {
		return nil, domain.Invalid("nothing to change")
	}
	return c.repo.UpdateResultCode(ctx, id, lbl, active)
}
