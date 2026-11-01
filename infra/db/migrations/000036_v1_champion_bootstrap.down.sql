-- Remove only the linkage introduced by this migration. Registry and fact
-- rows remain intact here: 000034.down removes evidence tables first, then
-- 000033.down removes governance objects in dependency-safe reverse order.
UPDATE engine_predictions ep
SET model_registry_id = NULL
FROM model_registry registry
WHERE ep.model_registry_id = registry.id
  AND registry.audit_metadata ->> 'bootstrap' = 'eng-003';

DROP FUNCTION IF EXISTS engine_prediction_market_family(TEXT);
