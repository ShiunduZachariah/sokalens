package notifications

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// EmailTransport is the fallback. It exists because spec §12's promise — "a
// Telegram send failure must never lose a notification" — is otherwise a
// slogan: with one transport, a Telegram outage means every notification
// retries into a dead endpoint and the operator learns nothing.
//
// It is deliberately a *peer* of TelegramTransport behind the same Transport
// interface, not a hook inside it, so neither transport knows the other exists
// and the worker's fallback policy lives in one readable place.
type EmailTransport struct {
	mailer Mailer
	from   string
	to     []string
}

var _ Transport = (*EmailTransport)(nil)

// Mailer is the SMTP seam. Keeping the wire protocol behind an interface is
// what lets the fallback be tested without a mail server, and what keeps this
// package on the standard library: SMTPMailer below is net/smtp, no dependency
// added.
type Mailer interface {
	Send(ctx context.Context, msg Mail) error
}

// Mail is one outbound message. Plain text only; this path runs when something
// else is already broken and a multipart body is one more thing to get wrong.
type Mail struct {
	From    string
	To      []string
	Subject string
	Body    string
}

// NewEmailTransport builds the fallback from the OPS_EMAIL_* configuration.
func NewEmailTransport(mailer Mailer, from string, to ...string) (*EmailTransport, error) {
	if mailer == nil {
		return nil, errors.New("notifications: email transport requires a mailer")
	}
	from = strings.TrimSpace(from)
	if !validAddress(from) {
		return nil, errors.New("notifications: email transport requires a valid from address")
	}
	recipients := make([]string, 0, len(to))
	for _, addr := range to {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if !validAddress(addr) {
			return nil, fmt.Errorf("notifications: invalid recipient address %q", addr)
		}
		recipients = append(recipients, addr)
	}
	if len(recipients) == 0 {
		return nil, errors.New("notifications: email transport requires at least one recipient")
	}
	return &EmailTransport{mailer: mailer, from: from, to: recipients}, nil
}

// Name is the ops_notification_outbox.transport value.
func (t *EmailTransport) Name() string { return TransportEmail }

// Deliver sends the notification as plain text.
func (t *EmailTransport) Deliver(ctx context.Context, n Notification) error {
	return t.mailer.Send(ctx, Mail{
		From:    t.from,
		To:      t.to,
		Subject: RenderEmailSubject(n),
		Body:    RenderEmailBody(n),
	})
}

// validAddress is a deliberately narrow check. It is not RFC 5322: it exists
// to reject anything that could carry a header injection or confuse the SMTP
// envelope, and an address this rejects is a configuration error worth
// failing loudly on.
func validAddress(addr string) bool {
	if addr == "" || len(addr) > 254 {
		return false
	}
	at := strings.IndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 || strings.LastIndexByte(addr, '@') != at {
		return false
	}
	if !strings.Contains(addr[at+1:], ".") {
		return false
	}
	for i := 0; i < len(addr); i++ {
		c := addr[i]
		if c <= ' ' || c == 0x7f || c == '<' || c == '>' || c == ',' || c == ';' || c == '"' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// net/smtp implementation.
// ---------------------------------------------------------------------------

// SMTPMailer is a working Mailer over net/smtp. No dependency is added: the
// standard library covers STARTTLS, implicit TLS and PLAIN auth, which is all
// any of the providers this service could use requires.
type SMTPMailer struct {
	// Addr is host:port, from OPS_EMAIL_SMTP_ADDRESS.
	Addr string
	// Username and Password are optional; an empty username skips AUTH, which
	// some relays on a private network require.
	Username string
	Password string
	// Timeout bounds the whole conversation when ctx carries no deadline.
	Timeout time.Duration
	// ImplicitTLS dials TLS directly rather than upgrading with STARTTLS. Set
	// it for port 465.
	ImplicitTLS bool
	// InsecureSkipVerify is never set in production. It exists so a
	// self-signed relay on a private network is a config decision rather than
	// a code change; wiring should refuse it when OPS_ENV is production.
	InsecureSkipVerify bool

	// dial is injectable for tests.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

var _ Mailer = (*SMTPMailer)(nil)

const defaultSMTPTimeout = 20 * time.Second

// Send delivers one message.
//
// net/smtp is not context-aware, so the context is honoured the only way it
// can be: it bounds the dial, and its deadline is pushed onto the connection
// so a server that accepts the TCP connection and then stops talking cannot
// hold a worker open. Without this a hung relay would pin one of the four
// workers indefinitely, which under an outage is exactly when they are needed.
func (m *SMTPMailer) Send(ctx context.Context, msg Mail) error {
	if m == nil || strings.TrimSpace(m.Addr) == "" {
		return errors.New("notifications: smtp address is not configured")
	}
	if len(msg.To) == 0 {
		return errors.New("notifications: smtp message has no recipients")
	}

	host, _, err := net.SplitHostPort(m.Addr)
	if err != nil {
		return fmt.Errorf("notifications: smtp address %q must be host:port: %w", m.Addr, err)
	}

	deadline := m.deadline(ctx)
	dialCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	conn, err := m.dialer()(dialCtx, "tcp", m.Addr)
	if err != nil {
		return fmt.Errorf("notifications: smtp dial: %w", err)
	}
	if m.ImplicitTLS {
		tlsConn := tls.Client(conn, m.tlsConfig(host))
		if err := tlsConn.HandshakeContext(dialCtx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("notifications: smtp tls handshake: %w", err)
		}
		conn = tlsConn
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("notifications: smtp deadline: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("notifications: smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	if !m.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(m.tlsConfig(host)); err != nil {
				return fmt.Errorf("notifications: smtp starttls: %w", err)
			}
		}
	}
	if m.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", m.Username, m.Password, host)); err != nil {
			return fmt.Errorf("notifications: smtp auth: %w", err)
		}
	}
	if err := client.Mail(msg.From); err != nil {
		return fmt.Errorf("notifications: smtp MAIL FROM: %w", err)
	}
	for _, rcpt := range msg.To {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("notifications: smtp RCPT TO: %w", err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("notifications: smtp DATA: %w", err)
	}
	if _, err := w.Write([]byte(BuildMessage(msg, timeNow()))); err != nil {
		_ = w.Close()
		return fmt.Errorf("notifications: smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notifications: smtp close body: %w", err)
	}
	// Quit waits for the server's 221. Only after it succeeds has the server
	// taken responsibility for the message, and only then may Deliver return
	// nil — a premature nil here would mark the outbox row delivered on a
	// message the relay never accepted.
	if err := client.Quit(); err != nil {
		return fmt.Errorf("notifications: smtp quit: %w", err)
	}
	return nil
}

func (m *SMTPMailer) dialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if m.dial != nil {
		return m.dial
	}
	d := &net.Dialer{}
	return d.DialContext
}

func (m *SMTPMailer) deadline(ctx context.Context) time.Time {
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = defaultSMTPTimeout
	}
	fallback := timeNow().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(fallback) {
		return d
	}
	return fallback
}

func (m *SMTPMailer) tlsConfig(host string) *tls.Config {
	return &tls.Config{
		ServerName:         host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: m.InsecureSkipVerify, //nolint:gosec // opt-in for a private relay; see the field comment
	}
}

// BuildMessage renders RFC 5322 headers plus body.
//
// Every header value passes through headerValue, which strips CR and LF. That
// is not defensive tidiness: a notification subject is assembled from incident
// data that originated outside this service, and a newline in it would let
// that data inject arbitrary headers — a Bcc, a different From — into an
// operational email.
func BuildMessage(msg Mail, at time.Time) string {
	var b strings.Builder
	b.WriteString("From: ")
	b.WriteString(headerValue(msg.From))
	b.WriteString("\r\nTo: ")
	b.WriteString(headerValue(strings.Join(msg.To, ", ")))
	b.WriteString("\r\nSubject: ")
	b.WriteString(headerValue(msg.Subject))
	b.WriteString("\r\nDate: ")
	b.WriteString(at.UTC().Format(time.RFC1123Z))
	b.WriteString("\r\nMIME-Version: 1.0")
	b.WriteString("\r\nContent-Type: text/plain; charset=UTF-8")
	b.WriteString("\r\nContent-Transfer-Encoding: 8bit")
	b.WriteString("\r\nAuto-Submitted: auto-generated")
	b.WriteString("\r\n\r\n")
	b.WriteString(dotStuff(normalizeNewlines(msg.Body)))
	if !strings.HasSuffix(msg.Body, "\n") {
		b.WriteString("\r\n")
	}
	return b.String()
}

func headerValue(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// dotStuff escapes a line consisting of a single "." so it cannot terminate
// the DATA command early and truncate the message.
func dotStuff(s string) string {
	if !strings.Contains(s, "\r\n.") && !strings.HasPrefix(s, ".") {
		return s
	}
	lines := strings.Split(s, "\r\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ".") {
			lines[i] = "." + line
		}
	}
	return strings.Join(lines, "\r\n")
}

// timeNow is a package-level seam so BuildMessage's Date header is testable.
var timeNow = time.Now
