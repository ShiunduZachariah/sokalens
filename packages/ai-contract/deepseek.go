package aicontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DeepSeekDefaultEndpoint = "https://api.deepseek.com/chat/completions"
	DeepSeekDefaultModel    = "deepseek-v4-flash"
)

type DeepSeekErrorKind string

const (
	DeepSeekErrorConfiguration   DeepSeekErrorKind = "configuration"
	DeepSeekErrorPaymentRequired DeepSeekErrorKind = "payment_required"
	DeepSeekErrorRateLimited     DeepSeekErrorKind = "rate_limited"
	DeepSeekErrorTimeout         DeepSeekErrorKind = "timeout"
	DeepSeekErrorCanceled        DeepSeekErrorKind = "canceled"
	DeepSeekErrorInvalidOutput   DeepSeekErrorKind = "invalid_output"
	DeepSeekErrorCircuitOpen     DeepSeekErrorKind = "circuit_open"
	DeepSeekErrorConcurrency     DeepSeekErrorKind = "concurrency_limit"
	DeepSeekErrorHTTPStatus      DeepSeekErrorKind = "http_status"
	DeepSeekErrorNetwork         DeepSeekErrorKind = "network"
)

type DeepSeekError struct {
	Kind       DeepSeekErrorKind
	StatusCode int
	RetryAfter time.Duration
	Stage      string
}

func (e *DeepSeekError) Error() string {
	switch e.Kind {
	case DeepSeekErrorHTTPStatus, DeepSeekErrorPaymentRequired, DeepSeekErrorRateLimited:
		return fmt.Sprintf("DeepSeek request failed: %s (HTTP %d)", e.Kind, e.StatusCode)
	default:
		if e.Stage != "" {
			return "DeepSeek request failed: " + string(e.Kind) + " (" + e.Stage + ")"
		}
		return "DeepSeek request failed: " + string(e.Kind)
	}
}

type DeepSeekPricing struct {
	InputMicrosPerMillionTokens  int64
	OutputMicrosPerMillionTokens int64
}

func (p DeepSeekPricing) validate() error {
	if p.InputMicrosPerMillionTokens < 0 || p.OutputMicrosPerMillionTokens < 0 || p.InputMicrosPerMillionTokens > 1_000_000_000_000 || p.OutputMicrosPerMillionTokens > 1_000_000_000_000 {
		return errors.New("DeepSeek token pricing is outside the supported range")
	}
	return nil
}

type DeepSeekConfig struct {
	APIKey                  string
	Endpoint                string
	Model                   string
	Timeout                 time.Duration
	MaxRequestBytes         int64
	MaxResponseBytes        int64
	MaxConcurrency          int
	MaxRetries              int
	MaxRetryDelay           time.Duration
	CircuitFailureThreshold int
	CircuitOpenDuration     time.Duration
	MaxTokens               int
	Pricing                 DeepSeekPricing
}

// DeepSeekConfigFromEnv loads only process environment configuration. Secret
// managers should inject DEEPSEEK_API_KEY into the process; the production
// library never reaches into platform-specific credential stores.
func DeepSeekConfigFromEnv() (DeepSeekConfig, error) {
	return deepSeekConfigFromLookup(os.Getenv)
}

func deepSeekConfigFromLookup(lookup func(string) string) (DeepSeekConfig, error) {
	config := DeepSeekConfig{
		APIKey:   lookup("DEEPSEEK_API_KEY"),
		Endpoint: lookup("DEEPSEEK_ENDPOINT"),
		Model:    lookup("DEEPSEEK_MODEL"),
	}
	parseDuration := func(name string, target *time.Duration) error {
		value := strings.TrimSpace(lookup(name))
		if value == "" {
			return nil
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid %s", name)
		}
		*target = parsed
		return nil
	}
	parseInt := func(name string, target *int) error {
		value := strings.TrimSpace(lookup(name))
		if value == "" {
			return nil
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid %s", name)
		}
		*target = parsed
		return nil
	}
	parseInt64 := func(name string, target *int64) error {
		value := strings.TrimSpace(lookup(name))
		if value == "" {
			return nil
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid %s", name)
		}
		*target = parsed
		return nil
	}
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{{"DEEPSEEK_TIMEOUT", &config.Timeout}, {"DEEPSEEK_MAX_RETRY_DELAY", &config.MaxRetryDelay}, {"DEEPSEEK_CIRCUIT_OPEN_DURATION", &config.CircuitOpenDuration}} {
		if err := parseDuration(item.name, item.target); err != nil {
			return DeepSeekConfig{}, err
		}
	}
	for _, item := range []struct {
		name   string
		target *int
	}{{"DEEPSEEK_MAX_CONCURRENCY", &config.MaxConcurrency}, {"DEEPSEEK_MAX_RETRIES", &config.MaxRetries}, {"DEEPSEEK_CIRCUIT_FAILURE_THRESHOLD", &config.CircuitFailureThreshold}, {"DEEPSEEK_MAX_TOKENS", &config.MaxTokens}} {
		if err := parseInt(item.name, item.target); err != nil {
			return DeepSeekConfig{}, err
		}
	}
	for _, item := range []struct {
		name   string
		target *int64
	}{{"DEEPSEEK_MAX_REQUEST_BYTES", &config.MaxRequestBytes}, {"DEEPSEEK_MAX_RESPONSE_BYTES", &config.MaxResponseBytes}, {"DEEPSEEK_INPUT_MICROS_PER_MILLION_TOKENS", &config.Pricing.InputMicrosPerMillionTokens}, {"DEEPSEEK_OUTPUT_MICROS_PER_MILLION_TOKENS", &config.Pricing.OutputMicrosPerMillionTokens}} {
		if err := parseInt64(item.name, item.target); err != nil {
			return DeepSeekConfig{}, err
		}
	}
	config = config.withDefaults()
	if err := config.validate(false); err != nil {
		return DeepSeekConfig{}, &DeepSeekError{Kind: DeepSeekErrorConfiguration}
	}
	return config, nil
}

func (c DeepSeekConfig) withDefaults() DeepSeekConfig {
	if c.Endpoint == "" {
		c.Endpoint = DeepSeekDefaultEndpoint
	}
	if c.Model == "" {
		c.Model = DeepSeekDefaultModel
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 64 << 10
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 32 << 10
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 4
	}
	if c.MaxRetryDelay == 0 {
		c.MaxRetryDelay = 2 * time.Second
	}
	if c.CircuitFailureThreshold == 0 {
		c.CircuitFailureThreshold = 3
	}
	if c.CircuitOpenDuration == 0 {
		c.CircuitOpenDuration = 30 * time.Second
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 1_000
	}
	return c
}

func (c DeepSeekConfig) validate(allowTestEndpoint bool) error {
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("DeepSeek API key is required")
	}
	parsed, err := url.Parse(c.Endpoint)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid DeepSeek endpoint")
	}
	if allowTestEndpoint {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("test DeepSeek endpoint must use HTTP or HTTPS")
		}
	} else if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "api.deepseek.com") || parsed.Port() != "" {
		return errors.New("DeepSeek endpoint must use HTTPS on api.deepseek.com")
	}
	if parsed.Path != "/chat/completions" {
		return errors.New("DeepSeek endpoint path must be /chat/completions")
	}
	if strings.TrimSpace(c.Model) == "" || len(c.Model) > 128 {
		return errors.New("DeepSeek model is required and must be at most 128 bytes")
	}
	if c.Timeout < time.Second || c.Timeout > 60*time.Second {
		return errors.New("DeepSeek timeout must be between 1s and 60s")
	}
	if c.MaxRequestBytes < 1_024 || c.MaxRequestBytes > 128<<10 {
		return errors.New("DeepSeek request limit must be between 1024 and 131072 bytes")
	}
	if c.MaxResponseBytes < 1_024 || c.MaxResponseBytes > 1<<20 {
		return errors.New("DeepSeek response limit must be between 1024 and 1048576 bytes")
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 64 {
		return errors.New("DeepSeek concurrency must be between 1 and 64")
	}
	if c.MaxRetries < 0 || c.MaxRetries > 2 {
		return errors.New("DeepSeek retries must be between 0 and 2")
	}
	if c.MaxRetryDelay < 0 || c.MaxRetryDelay > 10*time.Second {
		return errors.New("DeepSeek retry delay must be between 0s and 10s")
	}
	if c.CircuitFailureThreshold < 1 || c.CircuitFailureThreshold > 20 || c.CircuitOpenDuration < time.Second || c.CircuitOpenDuration > 10*time.Minute {
		return errors.New("invalid DeepSeek circuit breaker configuration")
	}
	if c.MaxTokens < 1 || c.MaxTokens > 8_192 {
		return errors.New("DeepSeek max tokens must be between 1 and 8192")
	}
	return c.Pricing.validate()
}

type DeepSeekUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostMicros       int64
	Attempts         int
	Complete         bool
}

type DeepSeekResult struct {
	Recommendation Recommendation
	Usage          DeepSeekUsage
	Model          string
	RequestID      string
}

type DeepSeekProvider struct {
	config DeepSeekConfig
	client *http.Client
	sem    chan struct{}
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error

	circuitMu        sync.Mutex
	circuitFailures  int
	circuitOpenUntil time.Time
}

var _ Provider = (*DeepSeekProvider)(nil)
var _ MeteredProvider = (*DeepSeekProvider)(nil)

func NewDeepSeekProvider(config DeepSeekConfig) (*DeepSeekProvider, error) {
	return newDeepSeekProvider(config, nil, false)
}

func newDeepSeekProvider(config DeepSeekConfig, client *http.Client, allowTestEndpoint bool) (*DeepSeekProvider, error) {
	config = config.withDefaults()
	if err := config.validate(allowTestEndpoint); err != nil {
		return nil, &DeepSeekError{Kind: DeepSeekErrorConfiguration}
	}
	if client == nil {
		client = &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("DeepSeek redirects are disabled")
		}}
	}
	p := &DeepSeekProvider{
		config: config,
		client: client,
		sem:    make(chan struct{}, config.MaxConcurrency),
		now:    time.Now,
	}
	p.sleep = func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	return p, nil
}

func (p *DeepSeekProvider) Recommend(ctx context.Context, evidence Evidence) (Recommendation, error) {
	result, err := p.RecommendWithMetadata(ctx, evidence)
	return result.Recommendation, err
}

func (p *DeepSeekProvider) MaxOutboundAttempts() int {
	return p.config.MaxRetries + 1
}

func (p *DeepSeekProvider) RecommendMetered(ctx context.Context, evidence Evidence) (Recommendation, ProviderUsage, error) {
	result, err := p.RecommendWithMetadata(ctx, evidence)
	usage := ProviderUsage{
		Attempts: result.Usage.Attempts, InputTokens: result.Usage.PromptTokens,
		OutputTokens: result.Usage.CompletionTokens, TotalTokens: result.Usage.TotalTokens,
		CostMicros: result.Usage.CostMicros, Complete: result.Usage.Complete,
	}
	return result.Recommendation, usage, err
}

func (p *DeepSeekProvider) RecommendWithMetadata(ctx context.Context, evidence Evidence) (DeepSeekResult, error) {
	if err := evidence.Validate(); err != nil {
		return DeepSeekResult{Usage: DeepSeekUsage{Complete: true}}, err
	}
	if err := p.enter(); err != nil {
		return DeepSeekResult{Usage: DeepSeekUsage{Complete: true}}, err
	}
	defer func() { <-p.sem }()

	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	body, err := p.requestBody(evidence)
	if err != nil {
		return DeepSeekResult{Usage: DeepSeekUsage{Complete: true}}, &DeepSeekError{Kind: DeepSeekErrorConfiguration, Stage: "request_size"}
	}

	var lastErr error
	usage := DeepSeekUsage{Complete: true}
	for attempt := 1; attempt <= p.config.MaxRetries+1; attempt++ {
		result, retry, retryAfter, err := p.attempt(ctx, body, evidence.IncidentID)
		usage.Attempts = attempt
		if result.Usage.Complete {
			usage.PromptTokens += result.Usage.PromptTokens
			usage.CompletionTokens += result.Usage.CompletionTokens
			usage.TotalTokens += result.Usage.TotalTokens
			usage.CostMicros += result.Usage.CostMicros
		} else if !isKnownZeroUsage(err) {
			usage.Complete = false
		}
		result.Usage = usage
		if err == nil {
			p.recordSuccess()
			return result, nil
		}
		lastErr = err
		if !retry || attempt > p.config.MaxRetries || retryAfter > p.config.MaxRetryDelay {
			p.recordFailure(err)
			return result, err
		}
		if retryAfter <= 0 {
			retryAfter = time.Duration(attempt) * 100 * time.Millisecond
		}
		if err := p.sleep(ctx, retryAfter); err != nil {
			lastErr = contextError(err)
			p.recordFailure(lastErr)
			return DeepSeekResult{Usage: usage}, lastErr
		}
	}
	p.recordFailure(lastErr)
	return DeepSeekResult{}, lastErr
}

func isKnownZeroUsage(err error) bool {
	var deepSeekErr *DeepSeekError
	if !errors.As(err, &deepSeekErr) {
		return false
	}
	switch deepSeekErr.Kind {
	case DeepSeekErrorPaymentRequired, DeepSeekErrorRateLimited, DeepSeekErrorHTTPStatus:
		return true
	default:
		return false
	}
}

func (p *DeepSeekProvider) enter() error {
	p.circuitMu.Lock()
	if !p.circuitOpenUntil.IsZero() {
		if p.now().Before(p.circuitOpenUntil) {
			p.circuitMu.Unlock()
			return &DeepSeekError{Kind: DeepSeekErrorCircuitOpen}
		}
		p.circuitOpenUntil = time.Time{}
		p.circuitFailures = 0
	}
	p.circuitMu.Unlock()
	select {
	case p.sem <- struct{}{}:
		return nil
	default:
		return &DeepSeekError{Kind: DeepSeekErrorConcurrency}
	}
}

func (p *DeepSeekProvider) recordSuccess() {
	p.circuitMu.Lock()
	p.circuitFailures = 0
	p.circuitOpenUntil = time.Time{}
	p.circuitMu.Unlock()
}

func (p *DeepSeekProvider) recordFailure(err error) {
	var providerErr *DeepSeekError
	if !errors.As(err, &providerErr) || providerErr.Kind == DeepSeekErrorCanceled || providerErr.Kind == DeepSeekErrorConcurrency || providerErr.Kind == DeepSeekErrorCircuitOpen {
		return
	}
	p.circuitMu.Lock()
	p.circuitFailures++
	if p.circuitFailures >= p.config.CircuitFailureThreshold {
		p.circuitOpenUntil = p.now().Add(p.config.CircuitOpenDuration)
	}
	p.circuitMu.Unlock()
}

type deepSeekRequest struct {
	Model          string            `json:"model"`
	Messages       []deepSeekMessage `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
	MaxTokens int `json:"max_tokens"`
}

type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (p *DeepSeekProvider) requestBody(evidence Evidence) ([]byte, error) {
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return nil, err
	}
	request := deepSeekRequest{
		Model: p.config.Model,
		Messages: []deepSeekMessage{
			{Role: "system", Content: `Return only one raw JSON object, with no markdown or code fences, using exactly this schema and no other fields: {"schema_version":"1","incident_id":"string copied exactly from input","classification":"short string","confidence":0.0,"actions":[{"operation":"observe_more|recommend_rollback|recommend_restart|escalate_human","target":"non-empty string","rationale":"non-empty string"}]}. Return 1 to 10 actions. Confidence must be between 0 and 1.`},
			{Role: "user", Content: "Produce a JSON recommendation for this pre-redacted evidence: " + string(evidenceJSON)},
		},
		MaxTokens: p.config.MaxTokens,
	}
	request.ResponseFormat.Type = "json_object"
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > p.config.MaxRequestBytes {
		return nil, errors.New("DeepSeek request exceeds configured limit")
	}
	return body, nil
}

type deepSeekResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message deepSeekMessage `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (p *DeepSeekProvider) attempt(ctx context.Context, body []byte, incidentID string) (DeepSeekResult, bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.config.Endpoint, bytes.NewReader(body))
	if err != nil {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorConfiguration}
	}
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	response, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return DeepSeekResult{}, false, 0, contextError(ctx.Err())
		}
		return DeepSeekResult{}, true, 0, &DeepSeekError{Kind: DeepSeekErrorNetwork}
	}
	defer response.Body.Close()
	retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), p.now())
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, p.config.MaxResponseBytes))
		switch response.StatusCode {
		case http.StatusPaymentRequired:
			return DeepSeekResult{}, false, retryAfter, &DeepSeekError{Kind: DeepSeekErrorPaymentRequired, StatusCode: response.StatusCode, RetryAfter: retryAfter}
		case http.StatusTooManyRequests:
			return DeepSeekResult{}, true, retryAfter, &DeepSeekError{Kind: DeepSeekErrorRateLimited, StatusCode: response.StatusCode, RetryAfter: retryAfter}
		default:
			retry := response.StatusCode == http.StatusBadGateway || response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout
			return DeepSeekResult{}, retry, retryAfter, &DeepSeekError{Kind: DeepSeekErrorHTTPStatus, StatusCode: response.StatusCode, RetryAfter: retryAfter}
		}
	}

	limited := io.LimitReader(response.Body, p.config.MaxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil || int64(len(responseBody)) > p.config.MaxResponseBytes {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "response_size"}
	}
	var decoded deepSeekResponse
	if err := decodeJSON(responseBody, &decoded, false); err != nil || len(decoded.Choices) != 1 {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "response_envelope"}
	}
	if strings.TrimSpace(decoded.ID) == "" || len(decoded.ID) > 256 || strings.TrimSpace(decoded.Model) == "" || len(decoded.Model) > 128 {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "response_envelope"}
	}
	content := decoded.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" || strings.Contains(content, "```") {
		return DeepSeekResult{}, true, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "empty_or_wrapped_content"}
	}
	var recommendation Recommendation
	if err := decodeJSON([]byte(content), &recommendation, true); err != nil || recommendation.IncidentID != incidentID || recommendation.Validate() != nil {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "recommendation_schema"}
	}
	if decoded.Usage == nil {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "usage_schema"}
	}
	usage := DeepSeekUsage{
		PromptTokens: decoded.Usage.PromptTokens, CompletionTokens: decoded.Usage.CompletionTokens, TotalTokens: decoded.Usage.TotalTokens,
		Complete: true,
	}
	if usage.PromptTokens <= 0 || usage.CompletionTokens <= 0 || usage.TotalTokens <= 0 || usage.TotalTokens > 100_000_000 || usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		return DeepSeekResult{}, false, 0, &DeepSeekError{Kind: DeepSeekErrorInvalidOutput, Stage: "usage_schema"}
	}
	usage.CostMicros = tokenCost(usage.PromptTokens, p.config.Pricing.InputMicrosPerMillionTokens) + tokenCost(usage.CompletionTokens, p.config.Pricing.OutputMicrosPerMillionTokens)
	return DeepSeekResult{Recommendation: recommendation, Usage: usage, Model: decoded.Model, RequestID: decoded.ID}, false, 0, nil
}

func tokenCost(tokens, microsPerMillion int64) int64 {
	if tokens == 0 || microsPerMillion == 0 {
		return 0
	}
	// Round up so accounting never understates fractional micro-costs.
	whole := (tokens / 1_000_000) * microsPerMillion
	remainder := tokens % 1_000_000
	return whole + (remainder*microsPerMillion+999_999)/1_000_000
}

func decodeJSON(data []byte, target any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return &DeepSeekError{Kind: DeepSeekErrorCanceled}
	}
	return &DeepSeekError{Kind: DeepSeekErrorTimeout}
}
