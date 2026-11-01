-- 000043_provider_pro_plan_defaults.up.sql
-- Bump the api-football quota baseline from the Free plan (100/day, 10/min) to the
-- Pro plan (7,500/day). Only rewrites rows that are still at the Free-plan defaults so
-- an already-reconciled paid value is preserved. daily_remaining/minute_remaining are
-- reset to full because a freshly issued Pro key starts with the full quota.
UPDATE provider_rate_limits
SET daily_limit = 7500,
    daily_remaining = 7500,
    minute_limit = 75,
    minute_remaining = 75,
    daily_reserve = 1500,
    minute_reserve = 15
WHERE provider = 'api-football'
  AND daily_limit = 100;
