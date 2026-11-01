package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const resendProvider = "resend"

type ResendTransport struct {
	APIKey   string
	BaseURL  string
	Client   *http.Client
	From     string
	FromName string
}

// Capabilities: Resend accepts a "headers" object on the send API, so it can
// carry List-Unsubscribe.
func (t ResendTransport) Capabilities() Capability { return CapabilityCustomHeaders }

// Ready reports whether this adapter is configured. The resolver calls it
// before routing, so an unconfigured provider is skipped in favour of the next
// one in the chain instead of consuming a delivery attempt.
func (t ResendTransport) Ready() error {
	if t.APIKey == "" || t.From == "" {
		return errors.New("Resend API key and from address are required")
	}
	return nil
}

func (t ResendTransport) Send(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	if err := t.Ready(); err != nil {
		return Delivery{}, newTransportError(resendProvider, FaultUnavailable, "not_ready", err)
	}
	var payload EmailPayload
	if err := json.Unmarshal(request.Message.Payload, &payload); err != nil {
		return Delivery{}, newTransportError(resendProvider, FaultPermanent, "payload", fmt.Errorf("decode email payload: %w", err))
	}
	if containsNewline(t.From) || containsNewline(t.FromName) || containsNewline(payload.To) || containsNewline(payload.Subject) || payload.To == "" || payload.Subject == "" {
		return Delivery{}, transportFault(resendProvider, FaultPermanent, "headers", "invalid email headers")
	}
	from := sanitizeHeader(t.From)
	if name := sanitizeHeader(t.FromName); name != "" {
		from = fmt.Sprintf("%s <%s>", name, from)
	}
	headers, err := unsubscribeHeaders(payload)
	if err != nil {
		return Delivery{}, newTransportError(resendProvider, FaultPermanent, "list_unsubscribe", err)
	}
	request_ := map[string]any{"from": from, "to": []string{sanitizeHeader(payload.To)}, "subject": sanitizeHeader(payload.Subject), "text": payload.Text, "html": payload.HTML}
	if len(headers) > 0 {
		request_["headers"] = headers
	}
	body, err := json.Marshal(request_)
	if err != nil {
		return Delivery{}, err
	}
	baseURL := strings.TrimRight(t.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.resend.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return Delivery{}, err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", request.IdempotencyKey)
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return Delivery{}, newTransportError(resendProvider, classifyDialError(err), "transport", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Delivery{}, newTransportError(resendProvider, classifyHTTPStatus(response.StatusCode), statusCode(response.StatusCode), fmt.Errorf("Resend returned HTTP %d", response.StatusCode))
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || result.ID == "" {
		// A 2xx we cannot parse may still have been accepted, so this must not
		// reach a second provider.
		return Delivery{}, transportFault(resendProvider, FaultAmbiguous, statusCode(response.StatusCode), "Resend response did not include a message ID")
	}
	return Delivery{Provider: resendProvider, ProviderMessageID: "resend:" + result.ID, ResponseCode: fmt.Sprint(response.StatusCode)}, nil
}
