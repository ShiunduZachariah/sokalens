package notifications

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const emailProviderSetting = "email.provider"

type ProviderSettingLookup interface {
	EmailProvider(context.Context) (string, error)
}

// RuntimeTransport routes each message to the administrator-selected email
// provider, and -- when that provider is demonstrably unable to accept it --
// to the next provider in an ordered fallback chain.
//
// The chain is what makes email delivery provider-independent in practice
// rather than on paper. Every adapter already sits behind Transport, so
// swapping providers is a settings change; but a settings change requires a
// human to notice, and a password reset that fails during an outage is not
// recoverable by noticing it an hour later.
//
// Only FaultUnavailable fails over (see fault.go). Ambiguous failures retry
// the same provider on the next outbox attempt, where the dedupe key is
// carried as the provider idempotency key and a duplicate send is suppressed
// by the provider itself.
type RuntimeTransport struct {
	Lookup          ProviderSettingLookup
	Providers       map[string]Transport
	DefaultProvider string
	// Fallbacks are tried, in order, after the active provider. Names that are
	// not registered or not ready are skipped rather than treated as an error.
	Fallbacks       []string
	RefreshInterval time.Duration
	Breaker         *ProviderBreaker
	Logger          *slog.Logger

	mu          sync.Mutex
	provider    string
	refreshedAt time.Time
}

func (t *RuntimeTransport) Send(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	// Decided once, before a provider is chosen: what this message needs in
	// order to be sent legitimately. A bulk message that must carry
	// List-Unsubscribe can only go to a provider that can carry headers, and
	// choosing the provider first would make that a per-adapter afterthought.
	required, err := messageRequirements(request.Message)
	if err != nil {
		return Delivery{}, err
	}
	chain := t.chain(ctx)
	if len(chain) == 0 {
		return Delivery{}, transportFault("", FaultUnavailable, "", "no email provider is registered and ready")
	}
	var failures []error
	for _, provider := range chain {
		transport := t.Providers[provider]
		if transport == nil {
			continue
		}
		if err := ProviderReady(transport); err != nil {
			failures = append(failures, newTransportError(provider, FaultUnavailable, "not_ready", err))
			continue
		}
		if !TransportCapabilities(transport).Has(required) {
			failures = append(failures, transportFault(provider, FaultUnavailable, "capability", "provider cannot carry the headers this message requires"))
			continue
		}
		if !t.Breaker.Allow(provider) {
			failures = append(failures, transportFault(provider, FaultUnavailable, "breaker_open", "provider is in cooldown after consecutive failures"))
			continue
		}
		delivery, err := transport.Send(ctx, request)
		if err == nil {
			t.Breaker.Success(provider)
			if delivery.Provider == "" {
				delivery.Provider = provider
			}
			return delivery, nil
		}
		if IsPermanentFault(err) {
			// The message, not the provider. Handing it to the next provider
			// would produce the same rejection and lose the reason.
			return Delivery{}, err
		}
		failures = append(failures, err)
		if !CanFailOver(err) {
			// Ambiguous: the message may already be on its way. Stop here and
			// let the outbox retry this same provider with the same
			// idempotency key.
			//
			// The breaker is deliberately NOT tripped here, and that is the
			// subtle part. An ambiguous failure is not evidence the provider is
			// down -- but if it counted, three timeouts would open the breaker,
			// and the next retry would find this provider skipped and fall
			// through to the next one. That is a duplicate send of a message
			// the first provider may already have accepted: the breaker would
			// manufacture exactly the failover ambiguity exists to prevent.
			return Delivery{}, errors.Join(failures...)
		}
		// Only a demonstrated refusal counts. The breaker exists to stop
		// spending requests on a provider that is known not to be accepting
		// them, which is what FaultUnavailable means and what ambiguity does
		// not.
		t.Breaker.Failure(provider)
		t.log(ctx, provider, err)
	}
	return Delivery{}, errors.Join(failures...)
}

func (t *RuntimeTransport) log(_ context.Context, provider string, err error) {
	logger := t.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// Provider name and fault class only. The error text can carry a recipient
	// or a token fragment, so it is categorised here rather than logged.
	logger.Warn("email provider unavailable, trying next provider",
		"provider", provider,
		"fault", string(FaultOf(err)),
	)
}

// chain returns the ordered provider names to attempt: the active provider
// first, then the configured fallbacks, deduplicated.
func (t *RuntimeTransport) chain(ctx context.Context) []string {
	ordered := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		if _, ok := t.Providers[name]; !ok {
			return
		}
		seen[name] = true
		ordered = append(ordered, name)
	}
	add(t.selected(ctx))
	for _, fallback := range t.Fallbacks {
		add(fallback)
	}
	return ordered
}

func (t *RuntimeTransport) selected(ctx context.Context) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	interval := t.RefreshInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if t.provider != "" && time.Since(t.refreshedAt) < interval {
		return t.provider
	}
	provider := strings.ToLower(strings.TrimSpace(t.DefaultProvider))
	if t.Lookup != nil {
		if configured, err := t.Lookup.EmailProvider(ctx); err == nil {
			configured = strings.ToLower(strings.TrimSpace(configured))
			if _, ok := t.Providers[configured]; ok {
				provider = configured
			}
		}
	}
	if _, ok := t.Providers[provider]; !ok {
		// Deliberately not a silent drop to "log". LogTransport reports every
		// message as delivered, so using it as an implicit last resort would
		// mark a production password reset "sent" while nothing left the
		// building. An unroutable message must surface as a delivery failure.
		provider = ""
	}
	t.provider = provider
	t.refreshedAt = time.Now()
	return provider
}

func (r Repository) EmailProvider(ctx context.Context) (string, error) {
	if r.db == nil {
		return "", errors.New("notification outbox DB is nil")
	}
	var provider string
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(value #>> '{}', '') FROM app_settings WHERE key = $1`, emailProviderSetting).Scan(&provider)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrProviderSettingNotFound
	}
	return provider, err
}

var ErrProviderSettingNotFound = errors.New("email provider setting not found")
