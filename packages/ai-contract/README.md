# AI provider contract

This package is the provider-neutral boundary for AI-assisted incident
recommendations. It accepts bounded, pre-redacted evidence and returns only
schema-validated, typed recommendations. It deliberately exposes no shell or
infrastructure mutation method.

`MockProvider` is deterministic and makes no network calls. `DeepSeekProvider`
implements the same recommendation-only boundary through DeepSeek's
OpenAI-compatible chat-completions endpoint. Production construction accepts
only `https://api.deepseek.com/chat/completions`; callers inject the API key
from their secret environment and should use `ValidatingProvider` at the
dispatch boundary. The adapter bounds time, response size, retries,
parallelism, and circuit failures and exposes token/cost metadata without
exposing prompts, response bodies, or credentials.

`DeepSeekConfigFromEnv` accepts `DEEPSEEK_API_KEY` and optional endpoint, model,
timeout, response-size, concurrency, retry, circuit-breaker, max-token, and
input/output micro-cost variables. Invalid configuration is rejected before
any network request. Production services should inject these variables from a
secret manager; no key belongs in source control or committed environment
files.

The current default model is `deepseek-v4-flash`. Model and per-million-token
prices are configuration, not embedded commercial assumptions. One manual
live smoke is available and skipped by default:

```powershell
$env:DEEPSEEK_LIVE_SMOKE = "1"
go test ./packages/ai-contract -run '^TestDeepSeekLiveSmoke$' -count=1 -v
```

On Windows the smoke test can read `DEEPSEEK_API_KEY` from the User environment
when the current process predates the variable. It prints only the model and
aggregate token count. Normal tests use `httptest` and make no external calls.

`BudgetPolicy` and `Manager` enforce validated per-incident token, cost,
attempt, and parallelism limits. Dispatch reservations are charged
conservatively so retries cannot evade their budget, and configured critical
capacity cannot be consumed by normal-priority work. Retry-capable providers
implement `MeteredProvider`: the workflow atomically reserves their maximum
outbound attempt, token, and cost envelope before dispatch, then settles it to
complete aggregate actual usage. Missing, malformed, overflowed, or
over-reservation telemetry fails closed and retains the worst-case charge.
Single-attempt generic providers remain supported and are conservatively
charged their full configured estimate.

The manager is deliberately process-local groundwork. Production scheduling
must not be enabled until incident claims/replays and spend accounting are
durable across concurrent controllers and process restarts.
