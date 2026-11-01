package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type entityMapDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type ProviderEntityMapRepository struct {
	db entityMapDB
}

type ProviderEntityMap struct {
	Provider          string
	EntityType        string
	ProviderEntityID  string
	ProviderEntityKey string
	CanonicalType     string
	CanonicalID       string
	Confidence        *float64
	MatchStatus       string
	Metadata          map[string]any
	SeenAt            time.Time
}

func NewProviderEntityMapRepository(db entityMapDB) ProviderEntityMapRepository {
	return ProviderEntityMapRepository{db: db}
}

func (r ProviderEntityMapRepository) Upsert(ctx context.Context, mapping ProviderEntityMap) error {
	if r.db == nil {
		return errors.New("provider entity map DB is nil")
	}
	if mapping.Provider == "" {
		return errors.New("provider is required")
	}
	if mapping.EntityType == "" {
		return errors.New("entity type is required")
	}
	if mapping.ProviderEntityID == "" {
		return errors.New("provider entity ID is required")
	}
	if mapping.MatchStatus == "" {
		mapping.MatchStatus = "unresolved"
	}
	if mapping.SeenAt.IsZero() {
		mapping.SeenAt = time.Now().UTC()
	}
	metadata, err := json.Marshal(mapping.Metadata)
	if err != nil {
		return err
	}
	if len(metadata) == 0 || string(metadata) == "null" {
		metadata = []byte(`{}`)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO provider_entity_maps (
  provider,
  entity_type,
  provider_entity_id,
  provider_entity_key,
  canonical_entity_type,
  canonical_entity_id,
  confidence,
  match_status,
  metadata,
  first_seen_at,
  last_seen_at
) VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9::jsonb, $10, $10)
ON CONFLICT (provider, entity_type, provider_entity_id) WHERE retired_at IS NULL
DO UPDATE SET
  provider_entity_key = EXCLUDED.provider_entity_key,
  canonical_entity_type = COALESCE(EXCLUDED.canonical_entity_type, provider_entity_maps.canonical_entity_type),
  canonical_entity_id = COALESCE(EXCLUDED.canonical_entity_id, provider_entity_maps.canonical_entity_id),
  confidence = COALESCE(EXCLUDED.confidence, provider_entity_maps.confidence),
  match_status = EXCLUDED.match_status,
  metadata = EXCLUDED.metadata,
  last_seen_at = EXCLUDED.last_seen_at`,
		mapping.Provider,
		mapping.EntityType,
		mapping.ProviderEntityID,
		mapping.ProviderEntityKey,
		mapping.CanonicalType,
		mapping.CanonicalID,
		mapping.Confidence,
		mapping.MatchStatus,
		string(metadata),
		mapping.SeenAt,
	)
	return err
}

func (r ProviderEntityMapRepository) LookupCanonicalEntityID(
	ctx context.Context,
	provider string,
	entityType string,
	providerEntityID string,
) (string, bool, error) {
	if r.db == nil {
		return "", false, errors.New("provider entity map DB is nil")
	}
	if provider == "" {
		return "", false, errors.New("provider is required")
	}
	if entityType == "" {
		return "", false, errors.New("entity type is required")
	}
	if providerEntityID == "" {
		return "", false, errors.New("provider entity ID is required")
	}

	var canonicalID sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT canonical_entity_id
FROM provider_entity_maps
WHERE
  provider = $1
  AND entity_type = $2
  AND provider_entity_id = $3
  AND retired_at IS NULL
LIMIT 1`,
		provider,
		entityType,
		providerEntityID,
	).Scan(&canonicalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !canonicalID.Valid || canonicalID.String == "" {
		return "", false, nil
	}
	return canonicalID.String, true, nil
}

// RepointCanonicalEntity changes only the canonical target and last-seen time.
// It preserves the provider key, confidence, and metadata gathered earlier.
func (r ProviderEntityMapRepository) RepointCanonicalEntity(
	ctx context.Context,
	provider string,
	entityType string,
	providerEntityID string,
	canonicalType string,
	canonicalID string,
	seenAt time.Time,
) error {
	if r.db == nil {
		return errors.New("provider entity map DB is nil")
	}
	if provider == "" || entityType == "" || providerEntityID == "" || canonicalType == "" || canonicalID == "" {
		return errors.New("provider and canonical entity identity are required")
	}
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE provider_entity_maps
SET canonical_entity_type = $4,
    canonical_entity_id = $5,
    match_status = 'resolved',
    last_seen_at = $6
WHERE provider = $1
  AND entity_type = $2
  AND provider_entity_id = $3
  AND retired_at IS NULL`, provider, entityType, providerEntityID, canonicalType, canonicalID, seenAt)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("provider entity map not found for repoint: %s/%s/%s", provider, entityType, providerEntityID)
	}
	return nil
}
