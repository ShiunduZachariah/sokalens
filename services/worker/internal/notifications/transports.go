package notifications

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LogTransport struct {
	Logger *slog.Logger
}

func (t LogTransport) Send(_ context.Context, request DeliveryRequest) (Delivery, error) {
	logger := t.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(
		"notification delivery logged",
		"outbox_id", request.Message.ID,
		"channel", request.Message.Channel,
		"category", request.Message.Category,
		"idempotency_key", request.IdempotencyKey,
	)
	return Delivery{Provider: "log", ProviderMessageID: "log:" + request.IdempotencyKey, ResponseCode: "logged"}, nil
}

const smtpProvider = "smtp"

// Capabilities: the log transport records whatever it is given, so it can
// carry headers. It is still refused as a production fallback in config,
// because recording a message is not delivering it.
func (t LogTransport) Capabilities() Capability { return CapabilityCustomHeaders }

type SMTPAuthFactory func() smtp.Auth

type SMTPSender func(addr string, auth smtp.Auth, from string, to []string, msg []byte) error

type SMTPTransport struct {
	Address    string
	From       string
	FromName   string
	Host       string
	Port       int
	Username   string
	Password   string
	Encryption string
	Auth       SMTPAuthFactory
	SendMail   SMTPSender
}

type EmailPayload struct {
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	HTML      string `json:"html,omitempty"`
	ReplyTo   string `json:"reply_to,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	// ListUnsubscribe is an absolute HTTPS URL that unsubscribes the recipient
	// on a POST with no further confirmation (RFC 8058). Its presence is what
	// advertises one-click, so it must not point at a page that asks the
	// reader to confirm.
	ListUnsubscribe string `json:"list_unsubscribe,omitempty"`
	// ListUnsubscribeMailto is the RFC 2369 mailto fallback, including the
	// "mailto:" scheme.
	ListUnsubscribeMailto string `json:"list_unsubscribe_mailto,omitempty"`
}

// Capabilities: the SMTP adapter assembles its own MIME message, so it can
// always carry custom headers.
func (t SMTPTransport) Capabilities() Capability { return CapabilityCustomHeaders }

// Ready reports whether this adapter is configured.
func (t SMTPTransport) Ready() error {
	if t.Address == "" && (t.Host == "" || t.Port == 0) || t.From == "" {
		return errors.New("SMTP address and from address are required")
	}
	return nil
}

func (t SMTPTransport) Send(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, newTransportError(smtpProvider, FaultAmbiguous, "context", err)
	}
	if err := t.Ready(); err != nil {
		return Delivery{}, newTransportError(smtpProvider, FaultUnavailable, "not_ready", err)
	}
	var payload EmailPayload
	if err := json.Unmarshal(request.Message.Payload, &payload); err != nil {
		return Delivery{}, newTransportError(smtpProvider, FaultPermanent, "payload", fmt.Errorf("decode email payload: %w", err))
	}
	if containsNewline(t.From) || containsNewline(t.FromName) || containsNewline(payload.To) || containsNewline(payload.Subject) {
		return Delivery{}, transportFault(smtpProvider, FaultPermanent, "headers", "SMTP headers must not contain newlines")
	}
	fromAddress := sanitizeHeader(t.From)
	from := fromAddress
	if name := sanitizeHeader(t.FromName); name != "" {
		from = fmt.Sprintf("%s <%s>", name, fromAddress)
	}
	payload.To = sanitizeHeader(payload.To)
	payload.Subject = sanitizeHeader(payload.Subject)
	if payload.To == "" || payload.Subject == "" {
		return Delivery{}, transportFault(smtpProvider, FaultPermanent, "headers", "email recipient and subject are required")
	}
	sendMail := t.SendMail
	if sendMail == nil {
		sendMail = smtp.SendMail
	}
	var auth smtp.Auth
	if t.Auth != nil {
		auth = t.Auth()
	}
	headers, headerErr := unsubscribeHeaders(payload)
	if headerErr != nil {
		return Delivery{}, newTransportError(smtpProvider, FaultPermanent, "list_unsubscribe", headerErr)
	}
	body := buildEmailMessage(from, payload, request.IdempotencyKey, headers)
	if t.SendMail != nil {
		if err := sendMail(t.Address, auth, fromAddress, []string{payload.To}, body); err != nil {
			return Delivery{}, classifySMTPError(err)
		}
	} else if err := t.send(ctx, fromAddress, payload.To, body); err != nil {
		return Delivery{}, classifySMTPError(err)
	}
	return Delivery{Provider: smtpProvider, ProviderMessageID: "smtp:" + request.IdempotencyKey, ResponseCode: "accepted"}, nil
}

func buildEmailMessage(from string, payload EmailPayload, idempotencyKey string, extraHeaders map[string]string) []byte {
	boundary := "sokalens-" + sanitizeMessageID(idempotencyKey)
	// Sorted so the rendered message is deterministic: the dedupe key is also
	// the provider idempotency key, and bytes that vary between attempts
	// defeat duplicate suppression at the receiving end.
	extra := ""
	for _, name := range sortedKeys(extraHeaders) {
		extra += sanitizeHeader(name) + ": " + sanitizeHeader(extraHeaders[name]) + "\r\n"
	}
	htmlBody := payload.HTML
	if strings.TrimSpace(htmlBody) == "" {
		htmlBody = "<p>" + strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\n", "<br>").Replace(payload.Text) + "</p>"
	}
	return []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <%s@sokalens.local>\r\nX-SokaLens-Request-ID: %s\r\n%sMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n--%s--\r\n", from, payload.To, payload.Subject, sanitizeMessageID(idempotencyKey), sanitizeHeader(payload.RequestID), extra, boundary, boundary, payload.Text, boundary, htmlBody, boundary))
}

func (t SMTPTransport) send(ctx context.Context, from, to string, body []byte) error {
	address := t.Address
	host := t.Host
	if address == "" {
		address = net.JoinHostPort(host, strconv.Itoa(t.Port))
	}
	if host == "" {
		host, _, _ = net.SplitHostPort(address)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if strings.EqualFold(t.Encryption, "tls") {
		plain, dialErr := dialer.DialContext(ctx, "tcp", address)
		if dialErr != nil {
			return dialErr
		}
		tlsConn := tls.Client(plain, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			_ = plain.Close()
			return err
		}
		conn = tlsConn
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return err
		}
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if strings.EqualFold(t.Encryption, "starttls") {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if t.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", t.Username, t.Password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	wc, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(body); err != nil {
		_ = wc.Close()
		return err
	}
	return completeSMTPDelivery(wc.Close, client.Quit)
}

func completeSMTPDelivery(closeData func() error, quit func() error) error {
	if err := closeData(); err != nil {
		return err
	}
	// A successful DATA close means the server accepted the message. A later
	// QUIT failure is ambiguous and must not cause an automatic duplicate send.
	_ = quit()
	return nil
}

// classifySMTPError maps an SMTP reply to a fault.
//
// 5xx is the server telling us the message is bad -- an unknown mailbox, a
// refused sender, a size limit. That verdict does not change with the next
// provider, so it is permanent. 4xx is a transient refusal (greylisting, a
// full queue), which means nothing was accepted and another provider may try.
// Anything else -- a socket closed mid-DATA, a TLS failure at an unknown point
// -- is ambiguous and must retry the same server rather than duplicate.
func classifySMTPError(err error) error {
	if err == nil {
		return nil
	}
	var reply *textproto.Error
	if errors.As(err, &reply) {
		kind := FaultAmbiguous
		switch {
		case reply.Code >= 500:
			kind = FaultPermanent
		case reply.Code >= 400:
			kind = FaultUnavailable
		}
		return newTransportError(smtpProvider, kind, statusCode(reply.Code), err)
	}
	return newTransportError(smtpProvider, classifyDialError(err), "transport", err)
}

func sortedKeys(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func containsNewline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func sanitizeHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", "", "\n", "").Replace(value))
}

func sanitizeMessageID(value string) string {
	value = sanitizeHeader(value)
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, value)
	if value == "" {
		return "notification"
	}
	return value
}
