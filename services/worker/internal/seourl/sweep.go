package seourl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// The sweep keeps seo_urls true as the catalogue moves underneath it.
//
// It is periodic rather than purely event-driven, and that is the design
// decision that matters most here. Three of the four transitions in a
// fixture's life have an event behind them -- it is created, a prediction
// publishes, its status resolves. The fourth does not:
//
//	played ──(fourteen days pass)──► archived
//
// Nothing fires. No row is written, no message is sent, no provider calls.
// Yesterday's indexable page is today's archived one purely because the clock
// moved, and only something that looks again will notice. An event-driven
// design would leave those pages advertised forever, which is the exact defect
// that failed the 2026-09-21 release.
//
// The same pass doubles as reconciliation for whatever the event path dropped,
// which matters because the event path will drop things.

// Batch bounds how many rows one sweep touches. A catalogue of 51,370 matches
// and 13,763 teams does not fit in one transaction anyone should hold, and a
// sweep that locks the table is a sweep that gets disabled.
const Batch = 2000

// StableSweepInterval is how often stable entities are re-derived.
//
// Teams, leagues and countries do not change their minds. Manchester United
// will still be Manchester United tomorrow, so re-deriving 13,763 rows every
// couple of minutes spends the whole sweep budget discovering that nothing
// happened. Once a day catches a renamed club or a new competition soon
// enough, because those arrive through the ingestion event path anyway and
// this is only the backstop.
const StableSweepInterval = 24 * time.Hour

// RetireAfter is how long a row may go unconfirmed before it is withdrawn.
//
// Deliberately generous. A row goes unconfirmed because its entity vanished --
// or because one sweep failed, or a deploy restarted the worker mid-pass, or
// the source query timed out. Retiring on the first miss would de-index the
// catalogue on any of those, and the recovery would be a full re-crawl at 275
// URLs a day. Three days means only a sustained absence counts.
const RetireAfter = 72 * time.Hour

// Store persists decisions. An interface so the sweep's logic can be tested
// without a database, because the parts worth testing are the batching, the
// retirement window and the ordering -- none of which need Postgres to be
// wrong.
type Store interface {
	// Upsert records decisions and stamps last_seen_at. Returns rows written.
	Upsert(ctx context.Context, rows []Row) (int, error)
	// RetireUnseen withdraws rows of the given volatility not confirmed since
	// the cutoff, and reports how many.
	RetireUnseen(ctx context.Context, volatile bool, before time.Time) (int, error)
}

// Source supplies the entities to decide about.
type Source interface {
	// Entities streams one volatility band. offset/limit page it.
	Entities(ctx context.Context, volatile bool, offset, limit int) ([]Entity, error)
}

// Row is a decided entity, ready to store.
type Row struct {
	Entity
	Decision
}

// Sweep re-derives the crawlable set.
type Sweep struct {
	Store  Store
	Source Source
	Logger *slog.Logger
	Now    func() time.Time
	// Batch overrides the page size. Zero means Batch.
	Batch int
	// MaxBatches bounds one run so a catalogue that has grown cannot turn a
	// routine sweep into an hour-long table scan.
	MaxBatches int
	// EnforceHistoryGate turns the fixture history gate from a measurement into
	// a decision.
	//
	// Off, a fixture that fails the gate is still advertised and merely
	// counted, so the population can be read off a real sweep before anything
	// is withheld. That matters because this gate is expected to remove
	// thousands of URLs, and the honest way to confirm it removes the long tail
	// rather than something structural is to look first.
	EnforceHistoryGate bool
}

// Result reports what one run did.
type Result struct {
	Examined  int
	Written   int
	Retired   int
	Truncated bool
	// HistoryGated counts fixtures that failed the history gate, whether or not
	// EnforceHistoryGate acted on them. In report-only mode this is the whole
	// point of the run.
	HistoryGated int
}

// Run re-derives one volatility band and retires what has gone.
func (s Sweep) Run(ctx context.Context, volatile bool) (Result, error) {
	if s.Store == nil || s.Source == nil {
		return Result{}, errors.New("seo url sweep needs a store and a source")
	}
	now := time.Now().UTC
	if s.Now != nil {
		now = func() time.Time { return s.Now().UTC() }
	}
	size := s.Batch
	if size <= 0 {
		size = Batch
	}
	maxBatches := s.MaxBatches
	if maxBatches <= 0 {
		maxBatches = 100
	}

	startedAt := now()
	result := Result{}
	for batch := 0; ; batch++ {
		if batch >= maxBatches {
			// Stopping short is not a failure. The next run resumes, and the
			// retirement window is three days, so a truncated pass cannot
			// de-index anything on its own.
			result.Truncated = true
			break
		}
		entities, err := s.Source.Entities(ctx, volatile, batch*size, size)
		if err != nil {
			return result, fmt.Errorf("load seo entities: %w", err)
		}
		if len(entities) == 0 {
			break
		}
		rows := make([]Row, 0, len(entities))
		for _, entity := range entities {
			result.Examined++
			// A malformed path is dropped rather than stored. The path is the
			// primary key, so storing one would either collide or render into
			// invalid sitemap XML, and neither failure is visible from here.
			if !ValidPath(entity.Path) {
				s.logger().Warn("skipping malformed seo path",
					"path", entity.Path, "entity_type", string(entity.Type), "entity_id", entity.ID)
				continue
			}
			decision := Decide(entity, startedAt)
			if decision.Reason == ReasonInsufficientHistory {
				result.HistoryGated++
				if !s.EnforceHistoryGate {
					// Report-only: counted above, but still advertised. The row
					// must carry no reason either, or the excluded_reason column
					// would disagree with the indexable flag beside it.
					decision = Decision{Indexable: true, Volatile: decision.Volatile}
				}
			}
			rows = append(rows, Row{Entity: entity, Decision: decision})
		}
		if len(rows) > 0 {
			written, err := s.Store.Upsert(ctx, rows)
			if err != nil {
				return result, fmt.Errorf("upsert seo urls: %w", err)
			}
			result.Written += written
		}
		if len(entities) < size {
			break
		}
	}

	// Retirement is skipped on a truncated pass. Rows this run never reached
	// look identical to rows whose entity has gone, and withdrawing them on
	// that evidence would de-index whatever happened to sort last.
	if !result.Truncated {
		retired, err := s.Store.RetireUnseen(ctx, volatile, startedAt.Add(-RetireAfter))
		if err != nil {
			return result, fmt.Errorf("retire unseen seo urls: %w", err)
		}
		result.Retired = retired
	}

	s.logger().Info("seo url sweep completed",
		"volatile", volatile,
		"examined", result.Examined,
		"written", result.Written,
		"retired", result.Retired,
		"truncated", result.Truncated,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
	return result, nil
}

func (s Sweep) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// DueForStableSweep reports whether the stable band should be re-derived.
//
// Separate from the volatile band because they run on different clocks, and
// because forgetting the distinction is how a sweep ends up re-deriving 13,763
// unchanged team rows every two minutes.
func DueForStableSweep(lastRun, now time.Time) bool {
	if lastRun.IsZero() {
		return true
	}
	return !now.Before(lastRun.Add(StableSweepInterval))
}
