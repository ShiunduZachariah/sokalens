-- 000043_provider_pro_plan_defaults.down.sql
-- Restore the Free-plan quota baseline for api-football if this migration bumped it.
UPDATE provider_rate_limits
SET daily_limit = 100,
    daily_remaining = 100,
    minute_limit = 10,
    minute_remaining = 10,
    daily_reserve = 20,
    minute_reserve = 2
WHERE provider = 'api-football'
  AND daily_limit = 7500
  AND minute_limit = 75;
