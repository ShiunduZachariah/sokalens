-- Registry rows cannot be deleted, and predictions already written keep their link
-- to python-engine-v3, so that identity is retired rather than removed.
UPDATE model_registry
SET status = 'retired',
    retired_at = now()
WHERE model_key = 'python-engine-v3'
  AND model_version = 'python-engine-v3'
  AND status = 'production';

UPDATE model_registry
SET status = 'production',
    retired_at = NULL
WHERE model_key = 'python-engine-v2'
  AND model_version = 'python-engine-v2'
  AND status = 'retired';
