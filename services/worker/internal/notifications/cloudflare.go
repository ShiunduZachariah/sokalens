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

const cloudflareProvider = "cloudflare"

type CloudflareTransport struct {
	AccountID string
	APIToken  string
	BaseURL   string
	Client    *http.Client
	From      string
	FromName  string
	// SupportsCustomHeaders must stay false until the account's Email Sending
	// API has been observed accepting a "headers" object.
	//
	// Declaring a capability we have not verified is the worse failure here.
	// If the API rejects the field, every bulk send fails at once; if it
	// ignores it, bulk mail ships without List-Unsubscribe and the damage
	// lands on the sending domain's reputation rather than on one message.
	// While this is false the resolver simply routes bulk mail to a provider
	// that can carry the header, which is exactly what the fallback chain is
	// for. Set from CLOUDFLARE_EMAIL_SUPPORTS_HEADERS after verifying.
	SupportsCustomHeaders bool
}

// Capabilities reports only what has been verified for this account.
func (t CloudflareTransport) Capabilities() Capability {
	if t.SupportsCustomHeaders {
		return CapabilityCustomHeaders
	}
	return 0
}

// Ready reports whether this adapter is configured.
func (t CloudflareTransport) Ready() error {
	if t.AccountID == "" || t.APIToken == "" || t.From == "" {
		return errors.New("Cloudflare account, API token, and from address are required")
	}
	return nil
}

func (t CloudflareTransport) Send(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	if err := t.Ready(); err != nil {
		return Delivery{}, newTransportError(cloudflareProvider, FaultUnavailable, "not_ready", err)
	}
	var payload EmailPayload
	if err := json.Unmarshal(request.Message.Payload, &payload); err != nil {
		return Delivery{}, newTransportError(cloudflareProvider, FaultPermanent, "payload", fmt.Errorf("decode email payload: %w", err))
	}
	if containsNewline(t.From) || containsNewline(t.FromName) || containsNewline(payload.To) || containsNewline(payload.Subject) || containsNewline(payload.ReplyTo) || payload.To == "" || payload.Subject == "" {
		return Delivery{}, transportFault(cloudflareProvider, FaultPermanent, "headers", "invalid email headers")
	}
	if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.HTML) == "" {
		return Delivery{}, transportFault(cloudflareProvider, FaultPermanent, "body", "email body is required")
	}
	from := sanitizeHeader(t.From)
	if name := sanitizeHeader(t.FromName); name != "" {
		from = fmt.Sprintf("%s <%s>", name, from)
	}
	headers, err := unsubscribeHeaders(payload)
	if err != nil {
		return Delivery{}, newTransportError(cloudflareProvider, FaultPermanent, "list_unsubscribe", err)
	}
	if len(headers) > 0 && !t.SupportsCustomHeaders {
		// The resolver filters on capability before reaching here; this is the
		// backstop for a direct caller. Dropping the header silently is what
		// must not happen.
		return Delivery{}, transportFault(cloudflareProvider, FaultUnavailable, "list_unsubscribe", "Cloudflare custom-header support is unverified for this account")
	}
	body := map[string]any{
		"from": from, "to": sanitizeHeader(payload.To), "subject": sanitizeHeader(payload.Subject),
		"text": payload.Text, "html": payload.HTML,
	}
	if replyTo := sanitizeHeader(payload.ReplyTo); replyTo != "" {
		body["reply_to"] = replyTo
	}
	if len(headers) > 0 {
		body["headers"] = headers
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return Delivery{}, err
	}
	baseURL := strings.TrimRight(t.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4"
	}
	url := fmt.Sprintf("%s/accounts/%s/email/sending/send", baseURL, t.AccountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return Delivery{}, err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIToken)
	req.Header.Set("Content-Type", "application/json")
	if request.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", request.IdempotencyKey)
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return Delivery{}, newTransportError(cloudflareProvider, classifyDialError(err), "transport", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Delivery{}, newTransportError(cloudflareProvider, classifyHTTPStatus(response.StatusCode), statusCode(response.StatusCode), fmt.Errorf("Cloudflare returned HTTP %d", response.StatusCode))
	}
	var result struct {
		Success bool `json:"success"`
		Result  struct {
			MessageID string `json:"message_id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || !result.Success || result.Result.MessageID == "" {
		// Accepted-but-unparseable. Ambiguous, so this never fails over.
		return Delivery{}, transportFault(cloudflareProvider, FaultAmbiguous, statusCode(response.StatusCode), "Cloudflare response did not include a message ID")
	}
	return Delivery{Provider: cloudflareProvider, ProviderMessageID: "cloudflare:" + result.Result.MessageID, ResponseCode: fmt.Sprint(response.StatusCode)}, nil
}
