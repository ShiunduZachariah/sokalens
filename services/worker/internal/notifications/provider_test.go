package notifications

import (
	"context"
	"errors"
	"testing"
)

type providerLookupFunc func(context.Context) (string, error)

func (f providerLookupFunc) EmailProvider(ctx context.Context) (string, error) { return f(ctx) }

type providerTransport struct{ id string }

func (p providerTransport) Send(context.Context, DeliveryRequest) (Delivery, error) {
	return Delivery{ProviderMessageID: p.id}, nil
}

func TestRuntimeTransportUsesConfiguredProviderAndCachesIt(t *testing.T) {
	lookups := 0
	runtime := RuntimeTransport{
		Lookup:          providerLookupFunc(func(context.Context) (string, error) { lookups++; return "cloudflare", nil }),
		Providers:       map[string]Transport{"log": providerTransport{"log"}, "cloudflare": providerTransport{"cloudflare"}},
		DefaultProvider: "log",
	}
	first, err := runtime.Send(context.Background(), DeliveryRequest{})
	if err != nil || first.ProviderMessageID != "cloudflare" {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	second, err := runtime.Send(context.Background(), DeliveryRequest{})
	if err != nil || second.ProviderMessageID != "cloudflare" || lookups != 1 {
		t.Fatalf("second = %+v, lookups=%d, err=%v", second, lookups, err)
	}
}

func TestRuntimeTransportFallsBackToLogForInvalidSetting(t *testing.T) {
	runtime := RuntimeTransport{
		Lookup:          providerLookupFunc(func(context.Context) (string, error) { return "smtp", errors.New("unavailable") }),
		Providers:       map[string]Transport{"log": providerTransport{"log"}, "cloudflare": providerTransport{"cloudflare"}},
		DefaultProvider: "log",
	}
	delivery, err := runtime.Send(context.Background(), DeliveryRequest{})
	if err != nil || delivery.ProviderMessageID != "log" {
		t.Fatalf("delivery = %+v, err=%v", delivery, err)
	}
}
