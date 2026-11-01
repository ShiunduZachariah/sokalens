package ingestion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type rawPayloadDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type RawPayloadRepository struct {
	db rawPayloadDB
}

type RawPayloadMetadata struct {
	Provider      string
	EndpointKey   string
	ParamsHash    string
	StatusCode    int
	ResponseHash  string
	ResponseBytes int
	Headers       map[string]string
	StoragePolicy string
	FetchedAt     time.Time
}

func NewRawPayloadRepository(db rawPayloadDB) RawPayloadRepository {
	return RawPayloadRepository{db: db}
}

func (r RawPayloadRepository) InsertMetadata(ctx context.Context, payload RawPayloadMetadata) error {
	if r.db == nil {
		return errors.New("raw payload DB is nil")
	}
	if payload.Provider == "" {
		return errors.New("provider is required")
	}
	if payload.EndpointKey == "" {
		return errors.New("endpoint key is required")
	}
	if payload.ResponseHash == "" {
		return errors.New("response hash is required")
	}
	if payload.StoragePolicy == "" {
		payload.StoragePolicy = "metadata_only"
	}
	if payload.FetchedAt.IsZero() {
		payload.FetchedAt = time.Now().UTC()
	}
	headers, err := json.Marshal(payload.Headers)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"storage_policy": payload.StoragePolicy,
		"response_bytes": payload.ResponseBytes,
	})
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO provider_raw_payloads (
  provider,
  endpoint_key,
  params_hash,
  status_code,
  response_hash,
  payload,
  headers,
  fetched_at,
  storage_policy,
  normalization_status
) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, 'pending')
ON CONFLICT (provider, endpoint_key, params_hash, response_hash) DO NOTHING`,
		payload.Provider,
		payload.EndpointKey,
		payload.ParamsHash,
		payload.StatusCode,
		payload.ResponseHash,
		string(body),
		string(headers),
		payload.FetchedAt,
		payload.StoragePolicy,
	)
	return err
}

func ResponseHash(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
