-- Called inside a short transaction after stopping the worker. Existing raw
-- or child rows win conflicts, preserving corrections and newer live writes.
WITH batch AS MATERIALIZED (
 SELECT * FROM raw_prediction_retention_recovery
 WHERE release_sha=:'release_sha'
 ORDER BY prediction_id LIMIT 1000 FOR UPDATE
), restored AS (
 INSERT INTO engine_predictions
 SELECT (jsonb_populate_record(NULL::engine_predictions,prediction_data)).* FROM batch
 ON CONFLICT(id) DO NOTHING RETURNING id
), results AS (
 INSERT INTO prediction_results
 SELECT (jsonb_populate_record(NULL::prediction_results,result_data)).* FROM batch
 WHERE result_data IS NOT NULL AND (
  EXISTS(SELECT 1 FROM restored WHERE id=batch.prediction_id)
  OR EXISTS(SELECT 1 FROM engine_predictions WHERE id=batch.prediction_id))
 ON CONFLICT(prediction_id) DO NOTHING RETURNING prediction_id
), trackers AS (
 INSERT INTO prediction_tracker_state
 SELECT (jsonb_populate_record(NULL::prediction_tracker_state,tracker_data)).* FROM batch
 WHERE tracker_data IS NOT NULL AND (
  EXISTS(SELECT 1 FROM restored WHERE id=batch.prediction_id)
  OR EXISTS(SELECT 1 FROM engine_predictions WHERE id=batch.prediction_id))
 ON CONFLICT(prediction_id) DO NOTHING RETURNING prediction_id
), cleared AS (
 DELETE FROM raw_prediction_retention_recovery recovery USING batch
 WHERE recovery.prediction_id=batch.prediction_id RETURNING recovery.prediction_id
), audit AS (
 INSERT INTO data_maintenance_audit(operation,criteria,rows_examined,rows_changed,details)
 SELECT 'raw_prediction_recovery',:'release_sha',count(*),
  (SELECT count(*) FROM restored)+(SELECT count(*) FROM results)+(SELECT count(*) FROM trackers),
  jsonb_build_object('release_sha',:'release_sha','archive_records',count(*),
   'raw_rows_inserted',(SELECT count(*) FROM restored),'result_rows_inserted',(SELECT count(*) FROM results),
   'tracker_rows_inserted',(SELECT count(*) FROM trackers))
 FROM cleared HAVING count(*)>0 RETURNING rows_changed
)
SELECT count(*) FROM cleared;
