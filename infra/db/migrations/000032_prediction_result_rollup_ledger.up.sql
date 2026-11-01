CREATE TABLE prediction_result_rollup_ledger (
  prediction_id UUID PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE RESTRICT,
  included_in_accuracy BOOLEAN NOT NULL,
  aggregated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_prediction_result_rollup_ledger_aggregated_at
  ON prediction_result_rollup_ledger (aggregated_at DESC);

COMMENT ON TABLE prediction_result_rollup_ledger IS
  'Compact idempotency ledger for prediction results already rolled into durable accuracy aggregates.';

COMMENT ON COLUMN prediction_result_rollup_ledger.included_in_accuracy IS
  'True only for the final eligible pre-kickoff prediction version counted in published accuracy.';
