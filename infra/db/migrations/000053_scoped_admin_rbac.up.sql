ALTER TABLE roles
  ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS is_assignable BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

ALTER TABLE permissions
  ADD COLUMN IF NOT EXISTS resource TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS action TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS risk_level TEXT NOT NULL DEFAULT 'LOW',
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE TABLE IF NOT EXISTS principal_role_assignments (
  id BIGSERIAL PRIMARY KEY,
  principal_type TEXT NOT NULL,
  principal_id TEXT NOT NULL,
  role_key TEXT NOT NULL REFERENCES roles(role_key) ON DELETE RESTRICT,
  scope_type TEXT NOT NULL DEFAULT 'GLOBAL',
  scope_id TEXT NOT NULL DEFAULT '',
  valid_from TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until TIMESTAMPTZ,
  created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  revoked_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  CHECK (principal_type IN ('HUMAN_ADMIN', 'SERVICE', 'SYSTEM')),
  CHECK (scope_type IN ('GLOBAL', 'COUNTRY', 'SPORT', 'COMPETITION', 'LEAGUE', 'ADVERTISER', 'CAMPAIGN')),
  CHECK (valid_until IS NULL OR valid_until > valid_from)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_principal_role_assignments_active_unique
  ON principal_role_assignments (principal_type, principal_id, role_key, scope_type, scope_id)
  WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_principal_role_assignments_resolve
  ON principal_role_assignments (principal_type, principal_id, valid_from, valid_until)
  WHERE revoked_at IS NULL;

INSERT INTO roles (role_key, name, description, admin_role, is_system, is_assignable) VALUES
  ('platform_owner', 'Platform owner', 'Break-glass platform authority; assign sparingly.', true, true, true),
  ('platform_admin', 'Platform administrator', 'Broad platform administration excluding owner-only authority.', true, true, true),
  ('operations_admin', 'Operations administrator', 'Provider, job, and operational controls.', true, true, true),
  ('content_editor', 'Content editor', 'Prediction and editorial content controls.', true, true, true),
  ('ads_manager', 'Advertising manager', 'Advertising and placement controls.', true, true, true),
  ('support_agent', 'Support agent', 'User support and session controls.', true, true, true),
  ('auditor', 'Auditor', 'Read-only audit and reporting access.', true, true, true)
ON CONFLICT (role_key) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  admin_role = EXCLUDED.admin_role,
  is_system = EXCLUDED.is_system,
  is_assignable = EXCLUDED.is_assignable,
  updated_at = now();

INSERT INTO permissions (permission_key, description, resource, action, risk_level) VALUES
  ('users.read', 'View user accounts', 'users', 'read', 'LOW'),
  ('users.manage', 'Suspend, restore, and revoke user sessions', 'users', 'manage', 'HIGH'),
  ('billing.read', 'Review subscriptions and billing state', 'billing', 'read', 'MEDIUM'),
  ('billing.manage', 'Manage billing configuration', 'billing', 'manage', 'CRITICAL'),
  ('content.manage', 'Manage published prediction content', 'content', 'manage', 'HIGH'),
  ('recommendations.manage', 'Manage recommendation policy', 'recommendations', 'manage', 'HIGH'),
  ('notifications.read', 'Review notification delivery', 'notifications', 'read', 'MEDIUM'),
  ('analytics.read', 'Read operational and growth analytics', 'analytics', 'read', 'LOW'),
  ('audit.read', 'Read append-only audit history', 'audit', 'read', 'LOW'),
  ('policy.manage', 'Manage geo and platform policy', 'policy', 'manage', 'CRITICAL')
ON CONFLICT (permission_key) DO UPDATE SET
  description = EXCLUDED.description,
  resource = EXCLUDED.resource,
  action = EXCLUDED.action,
  risk_level = EXCLUDED.risk_level;

INSERT INTO role_permissions (role_key, permission_key)
SELECT 'platform_owner', permission_key FROM permissions
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_key, permission_key) VALUES
  ('platform_admin', 'users.read'), ('platform_admin', 'users.manage'),
  ('platform_admin', 'billing.read'), ('platform_admin', 'billing.manage'),
  ('platform_admin', 'content.manage'), ('platform_admin', 'recommendations.manage'),
  ('platform_admin', 'notifications.read'), ('platform_admin', 'analytics.read'),
  ('platform_admin', 'audit.read'), ('platform_admin', 'policy.manage'),
  ('operations_admin', 'analytics.read'), ('operations_admin', 'audit.read'),
  ('operations_admin', 'policy.manage'), ('operations_admin', 'notifications.read'),
  ('content_editor', 'content.manage'), ('content_editor', 'recommendations.manage'),
  ('content_editor', 'analytics.read'),
  ('ads_manager', 'analytics.read'), ('ads_manager', 'policy.manage'),
  ('support_agent', 'users.read'), ('support_agent', 'users.manage'),
  ('support_agent', 'billing.read'), ('support_agent', 'notifications.read'),
  ('support_agent', 'audit.read'),
  ('billing_admin', 'billing.read'), ('billing_admin', 'billing.manage'),
  ('billing_admin', 'analytics.read'), ('billing_admin', 'audit.read'),
  ('auditor', 'users.read'), ('auditor', 'billing.read'),
  ('auditor', 'notifications.read'), ('auditor', 'analytics.read'), ('auditor', 'audit.read')
ON CONFLICT DO NOTHING;

INSERT INTO principal_role_assignments (principal_type, principal_id, role_key, scope_type, scope_id, created_by)
SELECT
  'HUMAN_ADMIN',
  u.id::text,
  CASE u.role
    WHEN 'super_admin' THEN 'platform_owner'
    WHEN 'admin' THEN 'platform_admin'
    WHEN 'support_admin' THEN 'support_agent'
    WHEN 'content_admin' THEN 'content_editor'
    WHEN 'billing_admin' THEN 'billing_admin'
    WHEN 'analyst' THEN 'auditor'
    WHEN 'viewer' THEN 'auditor'
  END,
  'GLOBAL',
  '',
  u.id
FROM users u
WHERE u.deleted_at IS NULL
  AND u.role IN ('super_admin', 'admin', 'support_admin', 'content_admin', 'billing_admin', 'analyst', 'viewer')
ON CONFLICT DO NOTHING;
