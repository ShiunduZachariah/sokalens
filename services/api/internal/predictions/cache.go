package predictions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

// Read-through cache for the prediction endpoints.
//
// /v1/predictions/today was failing 56% of requests: 5,630 of 10,066 calls in
// three hours returned 503 after exhausting the 2,750ms request budget. The
// database was not the reason. Measured on production, the four queries the
// handler runs cost about 85ms in total and Postgres sat idle between bursts;
// a single call served in 430ms. What broke it was arithmetic: a crawler
// walking fixture pages drove 400-880 requests a minute at that endpoint, and
// every one of them recomputed a response identical to the last.
//
// Two things fix that, and both are here.
//
// The cache means one computation serves every caller inside the TTL, which
// turns a burst of 880 into roughly three. The singleflight group means a
// *miss* under load still only runs once: without it, the moment a key expires
// every in-flight request queues behind its own copy of the same work, which
// is precisely the stampede that made this endpoint fail rather than merely be
// slow. The client's own cache could not help here -- a 503 stores nothing, so
// failure guaranteed the next request would also miss, and the spiral fed
// itself.
//
// Failures are never cached and cache errors never fail a request: Postgres
// stays the source of truth and Redis is only allowed to make it cheaper.
type Cache interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
}

// RedisCache adapts go-redis behind the narrow contract above.
type RedisCache struct{ Client *redis.Client }

func (r RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	if r.Client == nil {
		return nil, errors.New("redis cache unavailable")
	}
	return r.Client.Get(ctx, key).Bytes()
}

func (r RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if r.Client == nil {
		return errors.New("redis cache unavailable")
	}
	return r.Client.Set(ctx, key, value, ttl).Err()
}

// cacheSchema is part of every key. Bump it when a cached struct changes shape,
// so a deploy reads its own data instead of decoding yesterday's into today's
// fields and silently losing whatever was added.
const cacheSchema = "v1"

type CachedSource struct {
	origin    httpserver.PredictionSource
	cache     Cache
	namespace string
	ttl       time.Duration
	group     singleflight.Group
}

var _ httpserver.PredictionResultsSource = (*CachedSource)(nil)

func NewCachedSource(origin httpserver.PredictionSource, cache Cache, namespace string, ttl time.Duration) *CachedSource {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	if namespace == "" {
		namespace = "predictions"
	}
	return &CachedSource{origin: origin, cache: cache, namespace: namespace, ttl: ttl}
}

// TTLFromEnv reads a seconds value, falling back when it is absent or unusable.
func TTLFromEnv(raw string, fallback time.Duration) time.Duration {
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// CacheTTLFromEnv is the TTL this service should use, read once at startup.
//
// 60s, not the 15s this first shipped with. Measured on production: at 15s the
// cache held at most one live key at a time, so traffic arriving in bursts paid
// a cold miss on nearly every burst and the p50 stayed at 531ms. A prediction
// feed does not change meaningfully inside a minute, and the web revalidates
// its own copy every 15s regardless, so what a reader sees is no staler while
// the database work drops fourfold.
func CacheTTLFromEnv() time.Duration {
	return TTLFromEnv(os.Getenv("PREDICTIONS_REDIS_TTL_SECONDS"), 60*time.Second)
}

// loadBudget bounds the detached shared load. Deliberately longer than a single
// request budget: the work is being done on behalf of everyone waiting, and
// abandoning it early means they all fall back to the database individually --
// the stampede this exists to prevent.
const loadBudget = 10 * time.Second

func (s *CachedSource) key(method string, parts any) string {
	encoded, err := json.Marshal(parts)
	if err != nil {
		// An unhashable argument must not collide with a hashable one, and it
		// must not be served someone else's row either, so make it unique.
		return s.namespace + ":" + cacheSchema + ":" + method + ":unhashable:" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	sum := sha256.Sum256(encoded)
	return s.namespace + ":" + cacheSchema + ":" + method + ":" + hex.EncodeToString(sum[:16])
}

// read is the whole read-through path: cache, then a single shared load.
func read[T any](ctx context.Context, s *CachedSource, method string, parts any, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if s == nil || s.origin == nil {
		return zero, errors.New("prediction source unavailable")
	}
	if s.cache == nil {
		return load(ctx)
	}

	key := s.key(method, parts)
	if raw, err := s.cache.Get(ctx, key); err == nil && len(raw) > 0 {
		var hit T
		if json.Unmarshal(raw, &hit) == nil {
			return hit, nil
		}
		// A value that will not decode is worse than no value; fall through and
		// let the load below overwrite it.
	}

	// Everything that misses the same key at the same moment waits on one load.
	value, err, _ := s.group.Do(key, func() (any, error) {
		// The shared load must not inherit the cancellation of whichever caller
		// happened to arrive first. Forty requests wait on this one query; if
		// the winner's client disconnects, or its own 2,750ms budget expires a
		// moment before the query returns, an inherited context cancels the
		// query and every one of those waiters gets "context deadline exceeded"
		// for a result that was nearly ready. That is a stampede failing as a
		// group instead of individually, which is worse than no collapsing at
		// all, and it is what left a p95 of 2,751ms after the cache went in.
		//
		// So it runs detached with its own budget, and the waiters keep their
		// own deadlines: a caller that gives up still gives up, but it no
		// longer takes the shared work down with it.
		loadCtx, cancelLoad := context.WithTimeout(context.WithoutCancel(ctx), loadBudget)
		defer cancelLoad()

		fresh, loadErr := load(loadCtx)
		if loadErr != nil {
			return nil, loadErr
		}
		if encoded, marshalErr := json.Marshal(fresh); marshalErr == nil {
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			_ = s.cache.Set(writeCtx, key, encoded, s.ttl)
		}
		return fresh, nil
	})
	if err != nil {
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		return load(ctx)
	}
	return typed, nil
}

func (s *CachedSource) ListTodayPredictions(ctx context.Context, filter httpserver.PredictionFilter) ([]httpserver.PredictionSummary, error) {
	return read(ctx, s, "today.list", filter, func(ctx context.Context) ([]httpserver.PredictionSummary, error) {
		return s.origin.ListTodayPredictions(ctx, filter)
	})
}

func (s *CachedSource) CountTodayPredictions(ctx context.Context, filter httpserver.PredictionFilter) (int, error) {
	return read(ctx, s, "today.count", filter, func(ctx context.Context) (int, error) {
		return s.origin.CountTodayPredictions(ctx, filter)
	})
}

func (s *CachedSource) ListTodayProviderPredictions(ctx context.Context, filter httpserver.PredictionFilter) ([]httpserver.ProviderPredictionSummary, error) {
	return read(ctx, s, "today.provider.list", filter, func(ctx context.Context) ([]httpserver.ProviderPredictionSummary, error) {
		return s.origin.ListTodayProviderPredictions(ctx, filter)
	})
}

func (s *CachedSource) CountTodayProviderPredictions(ctx context.Context, filter httpserver.PredictionFilter) (int, error) {
	return read(ctx, s, "today.provider.count", filter, func(ctx context.Context) (int, error) {
		return s.origin.CountTodayProviderPredictions(ctx, filter)
	})
}

func (s *CachedSource) GetPrediction(ctx context.Context, id int64) (httpserver.PredictionDetail, error) {
	return read(ctx, s, "prediction.detail", id, func(ctx context.Context) (httpserver.PredictionDetail, error) {
		return s.origin.GetPrediction(ctx, id)
	})
}

func (s *CachedSource) GetHitRateReport(ctx context.Context) (httpserver.PredictionHitRateReport, error) {
	return read(ctx, s, "hitrate.report", "", func(ctx context.Context) (httpserver.PredictionHitRateReport, error) {
		return s.origin.GetHitRateReport(ctx)
	})
}

// Results are served directly from the source so the cache wrapper preserves
// the optional results endpoint without retaining settled data past a request.
func (s *CachedSource) ListPredictionResults(ctx context.Context, filter httpserver.PredictionFilter) ([]httpserver.PredictionResultSummary, error) {
	if s == nil {
		return nil, errors.New("prediction results source unavailable")
	}
	source, ok := s.origin.(httpserver.PredictionResultsSource)
	if !ok {
		return nil, errors.New("prediction results source unavailable")
	}
	return source.ListPredictionResults(ctx, filter)
}

func (s *CachedSource) CountPredictionResults(ctx context.Context, filter httpserver.PredictionFilter) (int, error) {
	if s == nil {
		return 0, errors.New("prediction results source unavailable")
	}
	source, ok := s.origin.(httpserver.PredictionResultsSource)
	if !ok {
		return 0, errors.New("prediction results source unavailable")
	}
	return source.CountPredictionResults(ctx, filter)
}
