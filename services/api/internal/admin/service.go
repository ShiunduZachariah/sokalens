package admin

import (
	"errors"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Role string

const (
	RoleSuperAdmin   Role = "super_admin"
	RoleAdmin        Role = "admin"
	RoleViewer       Role = "viewer"
	RoleSupportAdmin Role = "support_admin"
	RoleContentAdmin Role = "content_admin"
	RoleBillingAdmin Role = "billing_admin"
	RoleAnalyst      Role = "analyst"
)

type Permission string

const (
	PermissionManagePolicy          Permission = "policy.manage"
	PermissionReadAuditLogs         Permission = "audit.read"
	PermissionManageUsers           Permission = "users.manage"
	PermissionReadUsers             Permission = "users.read"
	PermissionReadBilling           Permission = "billing.read"
	PermissionManageBilling         Permission = "billing.manage"
	PermissionManageContent         Permission = "content.manage"
	PermissionManageRecommendations Permission = "recommendations.manage"
	PermissionReadNotifications     Permission = "notifications.read"
	PermissionReadAnalytics         Permission = "analytics.read"
)

type User struct {
	ID           int64
	Email        string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
}

type AuditEvent struct {
	Action   string
	ActorID  int64
	Entity   string
	EntityID string
	At       time.Time
}

type MemoryRepository struct {
	mu     sync.Mutex
	nextID int64
	users  map[string]User
	audits []AuditEvent
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		nextID: 1,
		users:  map[string]User{},
		audits: []AuditEvent{},
	}
}

func (m *MemoryRepository) saveUser(user User) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := strings.ToLower(user.Email)
	if _, exists := m.users[key]; exists {
		return User{}, errors.New("admin email already exists")
	}
	user.ID = m.nextID
	user.CreatedAt = time.Now().UTC()
	m.nextID++
	m.users[key] = user
	return user, nil
}

func (m *MemoryRepository) findByEmail(email string) (User, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	user, ok := m.users[strings.ToLower(email)]
	return user, ok
}

func (m *MemoryRepository) appendAudit(event AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	event.At = time.Now().UTC()
	m.audits = append(m.audits, event)
}

func (m *MemoryRepository) AuditEvents() []AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEvent, len(m.audits))
	copy(out, m.audits)
	return out
}

type Service struct {
	repo *MemoryRepository
}

func NewService(repo *MemoryRepository) Service {
	return Service{repo: repo}
}

func (s Service) CreateAdmin(email, password string, role Role, actorID int64) (User, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if len(password) < 12 {
		return User{}, errors.New("admin password must be at least 12 characters")
	}
	if role == "" {
		return User{}, errors.New("admin role is required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}

	user, err := s.repo.saveUser(User{
		Email:        normalized,
		PasswordHash: string(hash),
		Role:         role,
	})
	if err != nil {
		return User{}, err
	}

	s.repo.appendAudit(AuditEvent{
		Action:   "admin_user.created",
		ActorID:  actorID,
		Entity:   "admin_user",
		EntityID: normalized,
	})
	return user, nil
}

func (s Service) Authenticate(email, password string) (User, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	user, ok := s.repo.findByEmail(normalized)
	if !ok {
		return User{}, errors.New("invalid admin credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return User{}, errors.New("invalid admin credentials")
	}
	return user, nil
}

func Can(role Role, permission Permission) bool {
	if role == RoleSuperAdmin {
		return true
	}
	switch permission {
	case PermissionReadAuditLogs:
		return role == RoleAdmin || role == RoleViewer || role == RoleSupportAdmin || role == RoleBillingAdmin || role == RoleAnalyst
	case PermissionManageUsers, PermissionManagePolicy:
		return role == RoleAdmin || role == RoleSupportAdmin
	case PermissionReadUsers:
		return role == RoleAdmin || role == RoleSupportAdmin || role == RoleBillingAdmin || role == RoleAnalyst
	case PermissionReadBilling:
		return role == RoleAdmin || role == RoleSupportAdmin || role == RoleBillingAdmin || role == RoleAnalyst
	case PermissionManageBilling:
		return role == RoleAdmin || role == RoleBillingAdmin
	case PermissionManageContent:
		return role == RoleAdmin || role == RoleContentAdmin
	case PermissionManageRecommendations:
		return role == RoleAdmin || role == RoleContentAdmin
	case PermissionReadNotifications:
		return role == RoleAdmin || role == RoleSupportAdmin || role == RoleAnalyst
	case PermissionReadAnalytics:
		return role == RoleAdmin || role == RoleContentAdmin || role == RoleBillingAdmin || role == RoleAnalyst
	default:
		return false
	}
}

func normalizeEmail(email string) (string, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil {
		return "", errors.New("valid admin email is required")
	}
	return strings.ToLower(parsed.Address), nil
}
