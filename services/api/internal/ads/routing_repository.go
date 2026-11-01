package ads

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type SQLRepository struct{ db *sql.DB }

func NewSQLRepository(db *sql.DB) SQLRepository { return SQLRepository{db: db} }

func (repository SQLRepository) ListProviders(ctx context.Context) ([]Provider, error) {
	if repository.db == nil {
		return nil, errors.New("ads database unavailable")
	}
	rows, err := repository.db.QueryContext(ctx, `
SELECT code, name, adapter_key, enabled, global_priority, traffic_weight,
       health_status, last_health_check, failure_threshold, cooldown_seconds,
       request_timeout_ms, public_config
FROM ad_providers
ORDER BY global_priority ASC, code ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	providers := []Provider{}
	for rows.Next() {
		var provider Provider
		var lastHealthCheck sql.NullTime
		if err := rows.Scan(
			&provider.Code,
			&provider.Name,
			&provider.AdapterKey,
			&provider.Enabled,
			&provider.GlobalPriority,
			&provider.TrafficWeight,
			&provider.HealthStatus,
			&lastHealthCheck,
			&provider.FailureThreshold,
			&provider.CooldownSeconds,
			&provider.RequestTimeoutMS,
			&provider.PublicConfig,
		); err != nil {
			return nil, err
		}
		if lastHealthCheck.Valid {
			provider.LastHealthCheck = &lastHealthCheck.Time
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func (repository SQLRepository) RecordOutcome(
	ctx context.Context,
	sessionID string,
	attemptID string,
	placementKey string,
	providerCode string,
	outcome Outcome,
	failureReason FailureReason,
	durationMS int,
	occurredAt time.Time,
) error {
	if repository.db == nil {
		return errors.New("ads database unavailable")
	}
	digest := sha256.Sum256([]byte(sessionID))
	attemptDigest := sha256.Sum256([]byte(attemptID))
	result, err := repository.db.ExecContext(ctx, `
INSERT INTO ad_provider_outcomes (
	provider_code, placement_key, session_hash, attempt_hash, outcome,
	failure_reason, duration_ms, occurred_at
) VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8)
ON CONFLICT (attempt_hash) DO NOTHING`,
		providerCode,
		placementKey,
		hex.EncodeToString(digest[:]),
		hex.EncodeToString(attemptDigest[:]),
		string(outcome),
		string(failureReason),
		durationMS,
		occurredAt,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrDuplicateReport
	}
	return nil
}
