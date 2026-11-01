package acca

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is deliberately smaller than a Redis client. This keeps the read path
// testable and ensures cache failures are always safe (the database remains the
// source of truth).
type Cache interface {
	Get(context.Context, string) ([]byte, error)
	Set(context.Context, string, []byte, time.Duration) error
}

// RedisCache adapts go-redis while keeping the domain cache contract narrow.
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

type CachedSource struct {
	origin interface {
		List(context.Context, Query) (Result, error)
	}
	cache     Cache
	namespace string
	ttl       time.Duration
}

func NewCachedSource(origin interface {
	List(context.Context, Query) (Result, error)
}, cache Cache, namespace string, ttl time.Duration) *CachedSource {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &CachedSource{origin: origin, cache: cache, namespace: Namespace(namespace), ttl: ttl}
}

func (s *CachedSource) List(ctx context.Context, q Query) (Result, error) {
	if s == nil || s.origin == nil {
		return Result{}, errors.New("acca source unavailable")
	}
	if s.cache != nil {
		key := s.key(q)
		if raw, err := s.cache.Get(ctx, key); err == nil && len(raw) > 0 {
			var result Result
			if json.Unmarshal(raw, &result) == nil {
				result = sanitizeResult(result)
				return result, nil
			}
		}
		result, err := s.origin.List(ctx, q)
		if err != nil {
			return Result{}, err
		}
		if raw, err := json.Marshal(result); err == nil {
			_ = s.cache.Set(ctx, key, raw, s.ttl)
		}
		return result, nil
	}
	return s.origin.List(ctx, q)
}

func sanitizeResult(result Result) Result {
	valid := result.Items[:0]
	for _, item := range result.Items {
		if validSlip(item) {
			valid = append(valid, item)
		}
	}
	result.Items = valid
	return result
}

func (s *CachedSource) key(q Query) string {
	payload := struct {
		Query Query `json:"query"`
	}{q}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return Key(s.namespace, "result", hex.EncodeToString(digest[:]))
}

func TTLFromEnv(raw string, fallback time.Duration) time.Duration {
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}
