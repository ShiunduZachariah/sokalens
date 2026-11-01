DELETE FROM plan_prices
WHERE provider = 'sandbox' AND environment = 'local' AND plan_key IN ('pro_monthly','pro_yearly');

UPDATE subscription_plans
SET metadata = metadata - 'summary' - 'recommended' - 'savings_label', updated_at = now()
WHERE plan_key IN ('pro_monthly','pro_yearly');
