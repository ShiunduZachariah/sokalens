package ads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisStateStore struct {
	Client    *redis.Client
	Namespace string
}

func NewRedisStateStore(client *redis.Client, namespace string) RedisStateStore {
	namespace = strings.Trim(strings.TrimSpace(namespace), ":")
	if namespace == "" {
		namespace = "ads"
	}
	return RedisStateStore{Client: client, Namespace: namespace}
}

func (store RedisStateStore) sessionKey(sessionID string) string {
	return store.Namespace + ":session:" + sessionID
}

func (store RedisStateStore) reportKey(nonce string) string {
	return store.Namespace + ":report:" + nonce
}

func (store RedisStateStore) breakerKey(provider string) string {
	return store.Namespace + ":breaker:" + provider
}

func (store RedisStateStore) probeKey(provider string) string {
	return store.Namespace + ":breaker-probe:" + provider
}

func (store RedisStateStore) contributionKey(sessionID, provider string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return store.Namespace + ":breaker-contribution:" + provider + ":" + hex.EncodeToString(digest[:])
}

func (store RedisStateStore) LoadSession(ctx context.Context, sessionID string) (SessionState, error) {
	if store.Client == nil {
		return SessionState{}, errors.New("ads redis unavailable")
	}
	raw, err := store.Client.Get(ctx, store.sessionKey(sessionID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return SessionState{}, ErrStateNotFound
	}
	if err != nil {
		return SessionState{}, err
	}
	var state SessionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return SessionState{}, err
	}
	return state, nil
}

func (store RedisStateStore) CreateSession(ctx context.Context, sessionID string, initial SessionState, ttl time.Duration) (SessionState, error) {
	if store.Client == nil {
		return SessionState{}, errors.New("ads redis unavailable")
	}
	raw, err := json.Marshal(initial)
	if err != nil {
		return SessionState{}, err
	}
	created, err := store.Client.SetNX(ctx, store.sessionKey(sessionID), raw, ttl).Result()
	if err != nil {
		return SessionState{}, err
	}
	if created {
		return initial, nil
	}
	return store.LoadSession(ctx, sessionID)
}

func (store RedisStateStore) RecordFailure(ctx context.Context, sessionID, provider string, retryAfter time.Time, ttl time.Duration) (SessionState, error) {
	return store.updateSession(ctx, sessionID, ttl, func(state *SessionState) {
		state.FailedProviders = upsertFailedProvider(state.FailedProviders, provider, retryAfter)
	})
}

func (store RedisStateStore) RecordSuccess(ctx context.Context, sessionID, provider string, ttl time.Duration) (SessionState, error) {
	return store.updateSession(ctx, sessionID, ttl, func(state *SessionState) {
		state.ActiveProvider = provider
		state.FailedProviders = removeFailedProvider(state.FailedProviders, provider)
	})
}

func (store RedisStateStore) updateSession(ctx context.Context, sessionID string, ttl time.Duration, mutate func(*SessionState)) (SessionState, error) {
	if store.Client == nil {
		return SessionState{}, errors.New("ads redis unavailable")
	}
	key := store.sessionKey(sessionID)
	var updated SessionState
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		err = store.Client.Watch(ctx, func(transaction *redis.Tx) error {
			raw, err := transaction.Get(ctx, key).Bytes()
			if errors.Is(err, redis.Nil) {
				return ErrStateNotFound
			}
			if err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &updated); err != nil {
				return err
			}
			mutate(&updated)
			next, err := json.Marshal(updated)
			if err != nil {
				return err
			}
			_, err = transaction.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, next, ttl)
				return nil
			})
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			break
		}
	}
	return updated, err
}

func (store RedisStateStore) ClaimReport(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	if store.Client == nil {
		return false, errors.New("ads redis unavailable")
	}
	if ttl <= 0 {
		return false, nil
	}
	return store.Client.SetNX(ctx, store.reportKey(nonce), "1", ttl).Result()
}

func (store RedisStateStore) ClaimBreakerContribution(ctx context.Context, sessionID, provider string, ttl time.Duration) (bool, error) {
	if store.Client == nil {
		return false, errors.New("ads redis unavailable")
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return store.Client.SetNX(ctx, store.contributionKey(sessionID, provider), "1", ttl).Result()
}

// BreakerStates also performs the half-open probe claim. Once cooldown has
// elapsed, exactly one API replica receives an eligible half-open provider;
// other replicas keep skipping it until that probe reports success/failure.
func (store RedisStateStore) BreakerStates(ctx context.Context, providers []string, now time.Time) (map[string]BreakerState, error) {
	if store.Client == nil {
		return nil, errors.New("ads redis unavailable")
	}
	result := map[string]BreakerState{}
	for _, provider := range providers {
		values, err := store.Client.HGetAll(ctx, store.breakerKey(provider)).Result()
		if err != nil {
			return nil, err
		}
		openUntilMS, _ := strconv.ParseInt(values["open_until_ms"], 10, 64)
		if openUntilMS <= 0 {
			continue
		}
		openUntil := time.UnixMilli(openUntilMS).UTC()
		if now.Before(openUntil) {
			result[provider] = BreakerState{Provider: provider, OpenUntil: openUntil}
			continue
		}
		cooldownMS, _ := strconv.ParseInt(values["cooldown_ms"], 10, 64)
		cooldown := time.Duration(cooldownMS) * time.Millisecond
		if cooldown <= 0 {
			cooldown = 5 * time.Minute
		}
		claimed, err := store.Client.SetNX(ctx, store.probeKey(provider), "1", cooldown).Result()
		if err != nil {
			return nil, err
		}
		if claimed {
			result[provider] = BreakerState{Provider: provider, HalfOpen: true}
		} else {
			result[provider] = BreakerState{Provider: provider, OpenUntil: now.Add(cooldown)}
		}
	}
	return result, nil
}

var recordBreakerFailureScript = redis.NewScript(`
local open_until = tonumber(redis.call('HGET', KEYS[1], 'open_until_ms') or '0')
local failures = tonumber(redis.call('HGET', KEYS[1], 'failures') or '0')
local now_ms = tonumber(ARGV[1])
local threshold = tonumber(ARGV[2])
local cooldown_ms = tonumber(ARGV[3])

if open_until > 0 and open_until <= now_ms then
  failures = 0
  open_until = now_ms + cooldown_ms
elseif open_until <= 0 then
  failures = failures + 1
  if failures >= threshold then
    failures = 0
    open_until = now_ms + cooldown_ms
  end
end

redis.call('HSET', KEYS[1],
  'failures', failures,
  'open_until_ms', open_until,
  'cooldown_ms', cooldown_ms)
if open_until > now_ms then
  redis.call('PEXPIRE', KEYS[1], math.max(cooldown_ms * 3, 60000))
else
  redis.call('PEXPIRE', KEYS[1], math.max(cooldown_ms, 60000))
end
return open_until
`)

func (store RedisStateStore) RecordBreakerFailure(ctx context.Context, provider string, threshold int, cooldown time.Duration, now time.Time) error {
	if store.Client == nil {
		return errors.New("ads redis unavailable")
	}
	if threshold <= 0 {
		threshold = 20
	}
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}
	return recordBreakerFailureScript.Run(
		ctx,
		store.Client,
		[]string{store.breakerKey(provider)},
		now.UnixMilli(),
		threshold,
		cooldown.Milliseconds(),
	).Err()
}

func (store RedisStateStore) RecordBreakerSuccess(ctx context.Context, provider string) error {
	if store.Client == nil {
		return errors.New("ads redis unavailable")
	}
	// Ordinary closed-state successes do not erase the rolling unique-session
	// failure bucket. A success closes only an open/half-open breaker.
	values, err := store.Client.HGetAll(ctx, store.breakerKey(provider)).Result()
	if err != nil {
		return err
	}
	openUntilMS, _ := strconv.ParseInt(values["open_until_ms"], 10, 64)
	if openUntilMS <= 0 {
		return nil
	}
	return store.Client.Del(ctx, store.breakerKey(provider), store.probeKey(provider)).Err()
}
