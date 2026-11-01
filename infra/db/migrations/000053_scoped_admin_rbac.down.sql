DROP INDEX IF EXISTS idx_principal_role_assignments_resolve;
DROP INDEX IF EXISTS idx_principal_role_assignments_active_unique;
DROP TABLE IF EXISTS principal_role_assignments;

DELETE FROM role_permissions
WHERE role_key IN ('platform_owner', 'platform_admin', 'operations_admin', 'content_editor', 'ads_manager', 'support_agent', 'auditor');
DELETE FROM role_permissions
WHERE role_key = 'billing_admin'
  AND permission_key IN ('billing.read', 'billing.manage', 'analytics.read', 'audit.read');
DELETE FROM roles
WHERE role_key IN ('platform_owner', 'platform_admin', 'operations_admin', 'content_editor', 'ads_manager', 'support_agent', 'auditor');

ALTER TABLE permissions
  DROP COLUMN IF EXISTS created_at,
  DROP COLUMN IF EXISTS risk_level,
  DROP COLUMN IF EXISTS action,
  DROP COLUMN IF EXISTS resource;

ALTER TABLE roles
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS is_assignable,
  DROP COLUMN IF EXISTS is_system,
  DROP COLUMN IF EXISTS description;
