package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type scriptedTransport struct {
	name  string
	err   error
	calls *int
}

func (s scriptedTransport) Send(context.Context, DeliveryRequest) (Delivery, error) {
	if s.calls != nil {
		*s.calls++
	}
	if s.err != nil {
		return Delivery{}, s.err
	}
	return Delivery{ProviderMessageID: s.name + ":ok"}, nil
}

type unreadyTransport struct{ calls *int }

func (u unreadyTransport) Ready() error { return errors.New("missing API key") }

func (u unreadyTransport) Send(context.Context, DeliveryRequest) (Delivery, error) {
	*u.calls++
	return Delivery{ProviderMessageID: "unready:ok"}, nil
}

func TestRuntimeTransportFailsOverWhenProviderIsUnavailable(t *testing.T) {
	primary, fallback := 0, 0
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Providers: map[string]Transport{
			"cloudflare": scriptedTransport{name: "cloudflare", calls: &primary, err: transportFault("cloudflare", FaultUnavailable, "503", "service unavailable")},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}
	delivery, err := runtime.Send(context.Background(), DeliveryRequest{})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if delivery.ProviderMessageID != "resend:ok" || delivery.Provider != "resend" {
		t.Fatalf("delivery = %+v", delivery)
	}
	if primary != 1 || fallback != 1 {
		t.Fatalf("primary=%d fallback=%d", primary, fallback)
	}
}

func TestRuntimeTransportNeverFailsOverOnAnAmbiguousFailure(t *testing.T) {
	primary, fallback := 0, 0
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Providers: map[string]Transport{
			"cloudflare": scriptedTransport{name: "cloudflare", calls: &primary, err: transportFault("cloudflare", FaultAmbiguous, "", "connection reset after DATA")},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}
	if _, err := runtime.Send(context.Background(), DeliveryRequest{}); err == nil {
		t.Fatal("expected an error so the outbox retries the same provider")
	}
	// A message that may already have been accepted must not be offered to a
	// second provider: the dedupe key is only an idempotency key at the
	// provider that saw it first.
	if primary != 1 || fallback != 0 {
		t.Fatalf("primary=%d fallback=%d", primary, fallback)
	}
}

func TestRuntimeTransportNeverFailsOverOnAPermanentRejection(t *testing.T) {
	primary, fallback := 0, 0
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Providers: map[string]Transport{
			"cloudflare": scriptedTransport{name: "cloudflare", calls: &primary, err: transportFault("cloudflare", FaultPermanent, "422", "invalid recipient")},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}
	_, err := runtime.Send(context.Background(), DeliveryRequest{})
	if !IsPermanentFault(err) {
		t.Fatalf("err = %v, fault = %q", err, FaultOf(err))
	}
	if primary != 1 || fallback != 0 {
		t.Fatalf("primary=%d fallback=%d", primary, fallback)
	}
}

func TestRuntimeTransportSkipsUnconfiguredProviders(t *testing.T) {
	unready, fallback := 0, 0
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Providers: map[string]Transport{
			"cloudflare": unreadyTransport{calls: &unready},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}
	delivery, err := runtime.Send(context.Background(), DeliveryRequest{})
	if err != nil || delivery.Provider != "resend" {
		t.Fatalf("delivery = %+v, err = %v", delivery, err)
	}
	if unready != 0 {
		t.Fatalf("unconfigured provider was called %d times", unready)
	}
}

func TestRuntimeTransportDoesNotSilentlyDropToLog(t *testing.T) {
	logged := 0
	runtime := RuntimeTransport{
		// The operator selected Cloudflare, but its credentials are absent so
		// it was never registered. Routing to LogTransport here would mark a
		// password reset as sent while nothing left the building.
		DefaultProvider: "cloudflare",
		Providers:       map[string]Transport{"log": scriptedTransport{name: "log", calls: &logged}},
	}
	if _, err := runtime.Send(context.Background(), DeliveryRequest{}); err == nil {
		t.Fatal("expected an unroutable message to fail rather than be logged as delivered")
	}
	if logged != 0 {
		t.Fatalf("log transport was used %d times", logged)
	}
}

func TestProviderBreakerOpensAfterConsecutiveFailuresAndHalfOpens(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	breaker := &ProviderBreaker{FailureThreshold: 2, OpenFor: time.Minute, Now: func() time.Time { return now }}
	breaker.Failure("cloudflare")
	if !breaker.Allow("cloudflare") {
		t.Fatal("one failure must not open the breaker")
	}
	breaker.Failure("cloudflare")
	if breaker.Allow("cloudflare") || !breaker.Open("cloudflare") {
		t.Fatal("breaker should be open after the threshold")
	}
	now = now.Add(61 * time.Second)
	if !breaker.Allow("cloudflare") {
		t.Fatal("breaker should half-open once the cooldown elapses")
	}
	breaker.Success("cloudflare")
	if breaker.Open("cloudflare") {
		t.Fatal("a success should close the breaker")
	}
}

func TestRuntimeTransportSkipsAProviderWithAnOpenBreaker(t *testing.T) {
	primary, fallback := 0, 0
	breaker := &ProviderBreaker{FailureThreshold: 1, OpenFor: time.Minute}
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Breaker:         breaker,
		Providers: map[string]Transport{
			"cloudflare": scriptedTransport{name: "cloudflare", calls: &primary, err: transportFault("cloudflare", FaultUnavailable, "503", "down")},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}
	for range 3 {
		if _, err := runtime.Send(context.Background(), DeliveryRequest{}); err != nil {
			t.Fatalf("Send returned error: %v", err)
		}
	}
	// The failing provider is tried once, then held out of the chain for the
	// rest of the batch instead of costing every message a doomed request.
	if primary != 1 || fallback != 3 {
		t.Fatalf("primary=%d fallback=%d", primary, fallback)
	}
}

func TestHTTPStatusClassification(t *testing.T) {
	cases := map[int]FaultKind{
		408: FaultAmbiguous,
		429: FaultUnavailable,
		500: FaultUnavailable,
		503: FaultUnavailable,
		401: FaultUnavailable,
		403: FaultUnavailable,
		404: FaultUnavailable,
		400: FaultPermanent,
		422: FaultPermanent,
	}
	for status, want := range cases {
		if got := classifyHTTPStatus(status); got != want {
			t.Errorf("classifyHTTPStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestDialFailureIsUnavailableAndMidFlightFailureIsAmbiguous(t *testing.T) {
	if got := classifyDialError(&net.OpError{Op: "dial", Err: errors.New("connection refused")}); got != FaultUnavailable {
		t.Errorf("dial error = %q", got)
	}
	if got := classifyDialError(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}); got != FaultAmbiguous {
		t.Errorf("read error = %q", got)
	}
	if got := FaultOf(errors.New("something new")); got != FaultAmbiguous {
		t.Errorf("unclassified error = %q, want ambiguous", got)
	}
}

func TestResendClassifiesRejectionAsPermanentAndOutageAsUnavailable(t *testing.T) {
	payload, _ := json.Marshal(EmailPayload{To: "user@example.com", Subject: "Subject", Text: "Text"})
	for status, want := range map[int]FaultKind{http.StatusUnprocessableEntity: FaultPermanent, http.StatusServiceUnavailable: FaultUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		_, err := (ResendTransport{APIKey: "key", BaseURL: server.URL, From: "no-reply@sokalens.com", Client: server.Client()}).
			Send(context.Background(), DeliveryRequest{Message: Message{Payload: payload}, IdempotencyKey: "key"})
		if FaultOf(err) != want {
			t.Errorf("status %d: fault = %q, want %q (err=%v)", status, FaultOf(err), want, err)
		}
		server.Close()
	}
}

// The subtle one. An ambiguous failure must not trip the breaker.
//
// If it did, three mid-flight timeouts would open the breaker; the next retry
// would find the provider skipped and fall through to the next one -- sending
// a message the first provider may already have accepted. The breaker would
// manufacture precisely the failover that ambiguity exists to prevent, and it
// would do it only under repeated timeouts, which is exactly when a provider is
// most likely to have accepted something it never got to acknowledge.
func TestAmbiguousFailuresNeverOpenTheBreakerIntoAFailover(t *testing.T) {
	primary, fallback := 0, 0
	breaker := &ProviderBreaker{FailureThreshold: 2, OpenFor: time.Minute}
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Breaker:         breaker,
		Providers: map[string]Transport{
			"cloudflare": scriptedTransport{name: "cloudflare", calls: &primary, err: transportFault("cloudflare", FaultAmbiguous, "", "connection reset after DATA")},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}

	// Five attempts, well past the failure threshold.
	for range 5 {
		if _, err := runtime.Send(context.Background(), DeliveryRequest{}); err == nil {
			t.Fatal("an ambiguous failure reported success")
		}
	}

	if breaker.Open("cloudflare") {
		t.Fatal("ambiguous failures opened the breaker")
	}
	// Every attempt went to the same provider, and none reached the fallback.
	if primary != 5 {
		t.Fatalf("primary attempts = %d, want 5", primary)
	}
	if fallback != 0 {
		t.Fatalf("the fallback was reached %d times; a possibly-accepted message was re-sent", fallback)
	}
}

// The counterpart: a demonstrated refusal still opens it, because that is what
// the breaker is for.
func TestUnavailableFailuresStillOpenTheBreaker(t *testing.T) {
	primary, fallback := 0, 0
	breaker := &ProviderBreaker{FailureThreshold: 2, OpenFor: time.Minute}
	runtime := RuntimeTransport{
		DefaultProvider: "cloudflare",
		Fallbacks:       []string{"resend"},
		Breaker:         breaker,
		Providers: map[string]Transport{
			"cloudflare": scriptedTransport{name: "cloudflare", calls: &primary, err: transportFault("cloudflare", FaultUnavailable, "503", "down")},
			"resend":     scriptedTransport{name: "resend", calls: &fallback},
		},
	}
	for range 5 {
		if _, err := runtime.Send(context.Background(), DeliveryRequest{}); err != nil {
			t.Fatalf("Send returned error: %v", err)
		}
	}
	if !breaker.Open("cloudflare") {
		t.Fatal("repeated refusals did not open the breaker")
	}
	if primary != 2 || fallback != 5 {
		t.Fatalf("primary=%d fallback=%d", primary, fallback)
	}
}
