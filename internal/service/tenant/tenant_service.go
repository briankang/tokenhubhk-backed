package tenant

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"tokenhub-server/internal/model"
)

const maxTenantLevel = 3

// TenantService handles tenant/agent management with hierarchy support.
type TenantService struct {
	db *gorm.DB
}

// NewTenantService creates a new TenantService instance.
func NewTenantService(db *gorm.DB) *TenantService {
	if db == nil {
		panic("tenant service: db is nil")
	}
	return &TenantService{db: db}
}

// Create creates a new tenant. When ParentID is set, the tenant becomes a
// sub-agent with Level = parent.Level + 1 (capped at maxTenantLevel).
func (s *TenantService) Create(ctx context.Context, tenant *model.Tenant) error {
	if tenant == nil {
		return fmt.Errorf("tenant is nil")
	}
	if tenant.Name == "" {
		return fmt.Errorf("tenant name is required")
	}

	if tenant.ParentID != nil && *tenant.ParentID > 0 {
		var parent model.Tenant
		if err := s.db.WithContext(ctx).First(&parent, *tenant.ParentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("parent tenant not found")
			}
			return fmt.Errorf("failed to find parent tenant: %w", err)
		}
		if !parent.IsActive {
			return fmt.Errorf("parent tenant is inactive")
		}
		newLevel := parent.Level + 1
		if newLevel > maxTenantLevel {
			return fmt.Errorf("maximum tenant hierarchy depth (%d) exceeded", maxTenantLevel)
		}
		tenant.Level = newLevel
	} else {
		tenant.Level = 1
		tenant.ParentID = nil
	}

	tenant.IsActive = true
	if err := s.db.WithContext(ctx).Create(tenant).Error; err != nil {
		return fmt.Errorf("failed to create tenant: %w", err)
	}
	return nil
}

// GetByID returns a tenant with its children list.
func (s *TenantService) GetByID(ctx context.Context, id uint) (*model.Tenant, error) {
	if id == 0 {
		return nil, fmt.Errorf("tenant id is required")
	}
	var tenant model.Tenant
	if err := s.db.WithContext(ctx).Preload("Children").First(&tenant, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("tenant not found")
		}
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}
	return &tenant, nil
}

// List returns a paginated list of tenants. If parentID is provided, only
// children of that parent are returned; otherwise all top-level tenants.
func (s *TenantService) List(ctx context.Context, parentID *uint, page, pageSize int) ([]model.Tenant, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	var total int64
	query := s.db.WithContext(ctx).Model(&model.Tenant{})
	if parentID != nil {
		query = query.Where("parent_id = ?", *parentID)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count tenants: %w", err)
	}

	var tenants []model.Tenant
	offset := (page - 1) * pageSize
	if err := query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&tenants).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list tenants: %w", err)
	}

	return tenants, total, nil
}

// GetSubTree returns all descendant tenant IDs for a given tenant (BFS).
func (s *TenantService) GetSubTree(ctx context.Context, tenantID uint) ([]uint, error) {
	if tenantID == 0 {
		return nil, fmt.Errorf("tenant id is required")
	}

	result := []uint{tenantID}
	queue := []uint{tenantID}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		var children []model.Tenant
		if err := s.db.WithContext(ctx).Where("parent_id = ?", current).Find(&children).Error; err != nil {
			return nil, fmt.Errorf("failed to get children for tenant %d: %w", current, err)
		}
		for _, child := range children {
			result = append(result, child.ID)
			queue = append(queue, child.ID)
		}
	}

	return result, nil
}

// Update updates tenant fields by ID.
func (s *TenantService) Update(ctx context.Context, id uint, updates map[string]interface{}) error {
	if id == 0 {
		return fmt.Errorf("tenant id is required")
	}
	if len(updates) == 0 {
		return fmt.Errorf("no updates provided")
	}

	// Prevent updating structural fields
	delete(updates, "id")
	delete(updates, "parent_id")
	delete(updates, "level")

	res := s.db.WithContext(ctx).Model(&model.Tenant{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return fmt.Errorf("failed to update tenant: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("tenant not found")
	}
	return nil
}

// Deactivate deactivates a tenant and cascades to all child tenants.
func (s *TenantService) Deactivate(ctx context.Context, id uint) error {
	if id == 0 {
		return fmt.Errorf("tenant id is required")
	}

	ids, err := s.GetSubTree(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to get subtree: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&model.Tenant{}).Where("id IN ?", ids).Update("is_active", false).Error; err != nil {
		return fmt.Errorf("failed to deactivate tenants: %w", err)
	}

	// Also deactivate all users under these tenants
	if err := s.db.WithContext(ctx).Model(&model.User{}).Where("tenant_id IN ?", ids).Update("is_active", false).Error; err != nil {
		return fmt.Errorf("failed to deactivate tenant users: %w", err)
	}

	return nil
}

// GetByDomain finds a tenant by its custom domain (for white-label).
func (s *TenantService) GetByDomain(ctx context.Context, domain string) (*model.Tenant, error) {
	if domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	var tenant model.Tenant
	if err := s.db.WithContext(ctx).Where("domain = ? AND is_active = ?", domain, true).First(&tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("tenant not found for domain: %s", domain)
		}
		return nil, fmt.Errorf("failed to get tenant by domain: %w", err)
	}
	return &tenant, nil
}
