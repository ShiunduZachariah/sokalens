package aicontract

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestDeepSeekLiveSmoke is manual-only. It makes one minimal, redacted request
// and never prints the credential or response body. CI must not set the opt-in.
func TestDeepSeekLiveSmoke(t *testing.T) {
	if os.Getenv("DEEPSEEK_LIVE_SMOKE") != "1" {
		t.Skip("set DEEPSEEK_LIVE_SMOKE=1 for the one-shot live smoke")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		key = deepSeekUserEnvironmentKey()
	}
	if key == "" {
		t.Fatal("DEEPSEEK_API_KEY is not configured in the process or Windows User environment")
	}
	provider, err := NewDeepSeekProvider(DeepSeekConfig{APIKey: key, MaxRetries: 1, Timeout: 20 * time.Second, MaxTokens: 500})
	if err != nil {
		t.Fatal("DeepSeek provider configuration failed")
	}
	input := Evidence{SchemaVersion: SchemaVersion, IncidentID: "live-smoke", Redacted: true, Summary: "A synthetic health check reports elevated latency."}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	result, err := provider.RecommendWithMetadata(ctx, input)
	if err != nil {
		t.Fatalf("DeepSeek live smoke failed: %v", err)
	}
	if result.Recommendation.IncidentID != input.IncidentID {
		t.Fatal("DeepSeek live smoke returned the wrong incident")
	}
	t.Logf("DeepSeek live smoke passed: model=%s total_tokens=%d", result.Model, result.Usage.TotalTokens)
}
