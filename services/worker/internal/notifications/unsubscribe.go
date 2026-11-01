package notifications

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Unsubscribe headers, provider-neutrally.
//
// Gmail and Yahoo require RFC 8058 one-click unsubscribe on bulk mail, and a
// bulk message without it is not a style problem: it is rejected or foldered,
// and the reputation damage lands on the sending domain rather than on the
// message. That makes it a delivery correctness rule, which is why it lives
// here and not in a template.
//
// It is also the sharpest provider-capability difference in the stack. The
// SMTP adapter builds its own MIME headers and can always carry these; the
// REST adapters can only carry what their API accepts. Before this, neither
// REST adapter sent a custom header at all, so "swap the provider" and "still
// comply with the bulk-sender rules" were not the same statement. Declaring
// the capability makes the difference explicit: a provider that cannot carry
// the header is skipped for messages that need it, instead of silently
// sending non-compliant mail.

// Capability is what a transport can do beyond accepting a body.
type Capability uint8

const (
	// CapabilityCustomHeaders means the adapter can attach arbitrary RFC 5322
	// headers, which is what List-Unsubscribe requires.
	CapabilityCustomHeaders Capability = 1 << iota
)

// CapabilityReporter is opt-in. A transport that does not implement it is
// assumed to have no extra capabilities, so a new adapter is excluded from
// bulk sending until it says otherwise rather than included by omission.
type CapabilityReporter interface {
	Capabilities() Capability
}

func TransportCapabilities(transport Transport) Capability {
	if reporter, ok := transport.(CapabilityReporter); ok {
		return reporter.Capabilities()
	}
	return 0
}

func (c Capability) Has(want Capability) bool { return c&want == want }

// transactionalCategories are the messages a user cannot opt out of, because
// they only exist in response to something that user just did. Attaching an
// unsubscribe header to a password reset is not merely useless -- it offers to
// switch off the only channel that can recover the account.
var transactionalCategories = map[string]bool{
	"email_verification": true,
	"password_reset":     true,
}

// RequiresListUnsubscribe reports whether a message must carry unsubscribe
// headers. Anything on the email channel that is not explicitly transactional
// counts as bulk, so a category added later is covered by default rather than
// by remembering to add it.
func RequiresListUnsubscribe(channel, category string) bool {
	return channel == "email" && !transactionalCategories[category]
}

// unsubscribeHeaders renders the RFC 2369 and RFC 8058 headers for a payload.
//
// One-click is advertised only alongside an HTTPS URI, which is what RFC 8058
// requires: a mailto-only list cannot honour a POST, and claiming otherwise
// makes a mailbox provider's automated check fail.
func unsubscribeHeaders(payload EmailPayload) (map[string]string, error) {
	targets := []string{}
	oneClick := false
	if raw := strings.TrimSpace(payload.ListUnsubscribe); raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return nil, fmt.Errorf("list-unsubscribe URL must be absolute HTTPS")
		}
		if containsNewline(raw) || strings.ContainsAny(raw, "<>,") {
			return nil, fmt.Errorf("list-unsubscribe URL contains header delimiters")
		}
		targets = append(targets, "<"+raw+">")
		oneClick = true
	}
	if raw := strings.TrimSpace(payload.ListUnsubscribeMailto); raw != "" {
		if containsNewline(raw) || strings.ContainsAny(raw, "<>,") || !strings.HasPrefix(raw, "mailto:") {
			return nil, fmt.Errorf("list-unsubscribe mailto is not a bare mailto: URI")
		}
		targets = append(targets, "<"+raw+">")
	}
	if len(targets) == 0 {
		return nil, nil
	}
	headers := map[string]string{"List-Unsubscribe": strings.Join(targets, ", ")}
	if oneClick {
		headers["List-Unsubscribe-Post"] = "List-Unsubscribe=One-Click"
	}
	return headers, nil
}

// messageRequirements decides, once and provider-neutrally, what a message
// needs before any provider is chosen.
//
// Both directions are enforced. A bulk message without unsubscribe headers is
// refused rather than sent, because sending it damages the domain for every
// later message; a transactional message carrying them is refused too, because
// that is a producer bug and quietly stripping it would hide the bug.
func messageRequirements(message Message) (Capability, error) {
	if message.Channel != "email" {
		return 0, nil
	}
	var payload EmailPayload
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		// Adapters report their own decode errors with more context; an
		// undecodable payload is not this function's failure to report.
		return 0, nil
	}
	headers, err := unsubscribeHeaders(payload)
	if err != nil {
		return 0, newTransportError("", FaultPermanent, "list_unsubscribe", err)
	}
	bulk := RequiresListUnsubscribe(message.Channel, message.Category)
	if bulk && len(headers) == 0 {
		return 0, transportFault("", FaultPermanent, "list_unsubscribe",
			"bulk email must carry a List-Unsubscribe header; category "+message.Category+" is not transactional")
	}
	if !bulk && len(headers) > 0 {
		return 0, transportFault("", FaultPermanent, "list_unsubscribe",
			"transactional email must not offer unsubscribe; category "+message.Category+" is transactional")
	}
	if len(headers) == 0 {
		return 0, nil
	}
	return CapabilityCustomHeaders, nil
}
