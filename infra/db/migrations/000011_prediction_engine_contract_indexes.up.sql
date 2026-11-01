CREATE INDEX IF NOT EXISTS idx_fixtures_source_match
  ON fixtures (source_match_id)
  WHERE source_match_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_api_predictions_source_benchmark
  ON api_predictions (source_benchmark_id)
  WHERE source_benchmark_id IS NOT NULL;
