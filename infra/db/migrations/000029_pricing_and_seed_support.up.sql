UPDATE subscription_plans
SET metadata = metadata || CASE plan_key
  WHEN 'pro_monthly' THEN '{"summary":"Full SokaLens Pro access billed monthly.","recommended":false}'::jsonb
  WHEN 'pro_yearly' THEN '{"summary":"Full SokaLens Pro access billed annually.","recommended":true,"savings_label":"Save 16% with annual billing"}'::jsonb
  ELSE '{}'::jsonb
END,
updated_at = now()
WHERE plan_key IN ('pro_monthly','pro_yearly');

INSERT INTO plan_prices (plan_key, provider, environment, currency, amount_minor, active)
VALUES
  ('pro_monthly', 'sandbox', 'local', 'USD', 199, true),
  ('pro_yearly', 'sandbox', 'local', 'USD', 2000, true)
ON CONFLICT (plan_key, provider, environment, currency)
DO UPDATE SET amount_minor = EXCLUDED.amount_minor, active = EXCLUDED.active, updated_at = now();
