package ingestion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type oddsCatalogDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type OddsRepository struct {
	db oddsCatalogDB
}

type OddsSnapshotRecord struct {
	MatchID    int64
	MarketID   int64
	Provider   string
	Payload    map[string]any
	CapturedAt string
}

type PreparedSignal struct {
	MatchID  int64
	MarketID int64
	ModelID  int64
	Home     map[string]float64
	Away     map[string]float64
}

func NewOddsRepository(db oddsCatalogDB) OddsRepository {
	return OddsRepository{db: db}
}

func (r OddsRepository) LookupMarketID(ctx context.Context, marketKey string) (int64, error) {
	if r.db == nil {
		return 0, errors.New("odds repository DB is nil")
	}
	var marketID int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM odds_markets WHERE market_key = $1 LIMIT 1`, marketKey).Scan(&marketID)
	return marketID, err
}

func (r OddsRepository) LookupModelID(ctx context.Context, modelKey string) (int64, error) {
	if r.db == nil {
		return 0, errors.New("odds repository DB is nil")
	}
	var modelID int64
	err := r.db.QueryRowContext(ctx, `
SELECT id
FROM prediction_models
WHERE model_key = $1
ORDER BY id DESC
LIMIT 1`, modelKey).Scan(&modelID)
	return modelID, err
}

func (r OddsRepository) InsertOddsSnapshot(ctx context.Context, record OddsSnapshotRecord) error {
	if r.db == nil {
		return errors.New("odds repository DB is nil")
	}
	payload, err := json.Marshal(record.Payload)
	if err != nil {
		return err
	}
	provider := strings.TrimSpace(record.Provider)
	capturedAt := strings.TrimSpace(record.CapturedAt)
	payloadJSON := string(payload)
	var snapshotID int64
	snapshotID, err = r.lookupOddsSnapshotID(ctx, record.MatchID, record.MarketID, provider, capturedAt, payloadJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = r.db.QueryRowContext(ctx, `
INSERT INTO odds_snapshots (match_id, market_id, provider, payload, captured_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT DO NOTHING
RETURNING id`,
			record.MatchID,
			record.MarketID,
			provider,
			payloadJSON,
			capturedAt,
		).Scan(&snapshotID)
		if errors.Is(err, sql.ErrNoRows) {
			// A concurrent writer won the content-identity conflict. Reuse its
			// snapshot and upsert normalized prices below so retries can repair
			// a partially completed first attempt.
			snapshotID, err = r.lookupOddsSnapshotID(ctx, record.MatchID, record.MarketID, provider, capturedAt, payloadJSON)
		}
		if err != nil {
			return err
		}
	}
	record.Provider = provider
	record.CapturedAt = capturedAt
	return r.insertEngineOneXTwoOdds(ctx, snapshotID, record)
}

func (r OddsRepository) lookupOddsSnapshotID(ctx context.Context, matchID, marketID int64, provider, capturedAt, payload string) (int64, error) {
	var snapshotID int64
	err := r.db.QueryRowContext(ctx, `
SELECT id
FROM odds_snapshots
WHERE match_id = $1
  AND market_id = $2
  AND provider = $3
  AND captured_at = $4
  AND payload = $5
ORDER BY id
LIMIT 1`, matchID, marketID, provider, capturedAt, payload).Scan(&snapshotID)
	return snapshotID, err
}

func (r OddsRepository) insertEngineOneXTwoOdds(ctx context.Context, snapshotID int64, record OddsSnapshotRecord) error {
	bookmaker, _ := record.Payload["bookmaker"].(string)
	rawPrices, ok := record.Payload["prices"].([]oddsPricePayload)
	if !ok || strings.TrimSpace(bookmaker) == "" {
		return nil
	}
	prices := map[string]float64{}
	for _, price := range rawPrices {
		value, err := strconv.ParseFloat(strings.TrimSpace(price.Value), 64)
		if err != nil || value <= 0 {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(price.Label)) {
		case "home":
			prices["Home"] = value
		case "draw":
			prices["Draw"] = value
		case "away":
			prices["Away"] = value
		}
	}
	if len(prices) != 3 {
		return nil
	}
	homeImplied := 1 / prices["Home"]
	drawImplied := 1 / prices["Draw"]
	awayImplied := 1 / prices["Away"]
	total := homeImplied + drawImplied + awayImplied
	if total <= 0 {
		return nil
	}
	for _, outcome := range []string{"Home", "Draw", "Away"} {
		_, err := r.db.ExecContext(ctx, `
INSERT INTO odds (
  fixture_id,
  bookmaker,
  market,
  outcome,
  value,
  implied_prob,
  fetched_at,
  source_snapshot_id
) VALUES ($1, $2, '1x2', $3, $4, $5, $6, $7)
ON CONFLICT (source_snapshot_id, outcome) DO UPDATE SET
  value = EXCLUDED.value,
  implied_prob = EXCLUDED.implied_prob,
  fetched_at = EXCLUDED.fetched_at`,
			record.MatchID,
			strings.TrimSpace(bookmaker),
			outcome,
			prices[outcome],
			(1/prices[outcome])/total,
			record.CapturedAt,
			snapshotID,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r OddsRepository) UpsertFeatureSnapshot(ctx context.Context, signal PreparedSignal) error {
	if r.db == nil {
		return errors.New("odds repository DB is nil")
	}
	payload, err := json.Marshal(map[string]any{
		"home": signal.Home,
		"away": signal.Away,
	})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	snapshotHash := hex.EncodeToString(sum[:])
	_, err = r.db.ExecContext(ctx, `
INSERT INTO prediction_feature_snapshots (
  match_id,
  market_id,
  model_id,
  feature_key,
  snapshot_hash,
  payload
) VALUES ($1, $2, $3, 'odds_signal', $4, $5::jsonb)
ON CONFLICT (match_id, market_id, model_id, snapshot_hash)
DO NOTHING`,
		signal.MatchID,
		signal.MarketID,
		signal.ModelID,
		snapshotHash,
		string(payload),
	)
	return err
}

func (r OddsRepository) RecordGenerationFailure(ctx context.Context, matchID, marketID, modelID int64, reasonCode, detail string) error {
	if r.db == nil {
		return errors.New("odds repository DB is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO prediction_generation_failures (
  match_id,
  market_id,
  model_id,
  reason_code,
  detail
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (match_id, market_id, model_id)
DO UPDATE SET
  reason_code = EXCLUDED.reason_code,
  detail = EXCLUDED.detail,
  updated_at = now()`,
		matchID,
		marketID,
		modelID,
		strings.TrimSpace(reasonCode),
		strings.TrimSpace(detail),
	)
	return err
}

func (r OddsRepository) ClearGenerationFailure(ctx context.Context, matchID, marketID, modelID int64) error {
	if r.db == nil {
		return errors.New("odds repository DB is nil")
	}
	_, err := r.db.ExecContext(ctx, `
DELETE FROM prediction_generation_failures
WHERE match_id = $1 AND market_id = $2 AND model_id = $3`,
		matchID,
		marketID,
		modelID,
	)
	return err
}

func buildSignalsFromOdds(homeOdds float64, drawOdds float64, awayOdds float64) (PreparedSignal, error) {
	if homeOdds <= 0 || drawOdds <= 0 || awayOdds <= 0 {
		return PreparedSignal{}, fmt.Errorf("odds must be positive")
	}
	homeImplied := 1 / homeOdds
	drawImplied := 1 / drawOdds
	awayImplied := 1 / awayOdds
	total := homeImplied + drawImplied + awayImplied
	if total <= 0 {
		return PreparedSignal{}, fmt.Errorf("invalid implied odds total")
	}
	homeProb := homeImplied / total
	drawProb := drawImplied / total
	awayProb := awayImplied / total
	return PreparedSignal{
		Home: map[string]float64{
			"attack_rating":  homeProb,
			"defense_rating": 1 - awayProb,
			"form_rating":    homeProb + drawProb/2,
		},
		Away: map[string]float64{
			"attack_rating":  awayProb,
			"defense_rating": 1 - homeProb,
			"form_rating":    awayProb + drawProb/2,
		},
	}, nil
}
