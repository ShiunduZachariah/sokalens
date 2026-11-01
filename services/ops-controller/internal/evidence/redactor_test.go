package evidence

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareRedactsSensitiveValuesBeforeDispatch(t *testing.T) {
	b := validBundle()
	sensitive := []Label{
		{Name: "authorization", Value: "Bearer abcdefghijklmnopqrstuvwxyz"},
		{Name: "email", Value: "person@example.com"},
		{Name: "route", Value: "password=hunter2"},
		{Name: "api_key", Value: "api-key-sentinel-123"},
		{Name: "cookie", Value: "cookie-sentinel-456"},
		{Name: "reset_token", Value: "reset-sentinel-789"},
		{Name: "session", Value: "session-sentinel-012"},
		{Name: "totp_secret", Value: "totp-sentinel-345"},
		{Name: "otp", Value: "otp-sentinel-678"},
		{Name: "2fa", Value: "twofa-sentinel-901"},
		{Name: "payment_token", Value: "payment-sentinel-234"},
		{Name: "user_id", Value: "user-sentinel-567"},
		{Name: "account_id", Value: "account-sentinel-890"},
		{Name: "db", Value: "postgresql://admin:secret@db.internal/sokalens"},
		{Name: "provider", Value: "re_abcdefghijklmnopqrstuvwxyz"},
		{Name: "jwt", Value: "eyJabcdefghijk.abcdefghijkl.abcdefghijkl"},
		{Name: "card", Value: "4111 1111 1111 1111"},
	}
	b.Records[0].Metric.Labels = sensitive[:MaxLabelsPerMetric]
	extra := *b.Records[0].Metric
	extra.Labels = sensitive[MaxLabelsPerMetric:]
	b.Records = append(b.Records, Reference{ID: "fixture-prometheus-sensitive", Kind: KindMetric, Source: SourcePrometheus, ObservedAt: testTime(), Metric: &extra})
	b.Records[2].Trace.ErrorClass = "user_id=usr-123"
	prepared, err := Prepare(b)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	got := prepared.Value()
	for _, record := range got.Records {
		if record.Metric == nil {
			continue
		}
		for _, label := range record.Metric.Labels {
			if label.Value != Redacted {
				t.Errorf("label %q value = %q, want redacted", label.Name, label.Value)
			}
		}
	}
	if got.Records[2].Trace.ErrorClass != Redacted {
		t.Errorf("user identifier = %q, want redacted", got.Records[2].Trace.ErrorClass)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.ToLower(string(encoded))
	for _, secret := range []string{
		"hunter2", "person@example.com", "postgresql://", "4111", "abcdefghijklmnopqrstuvwxyz", "usr-123",
		"api-key-sentinel", "cookie-sentinel", "reset-sentinel", "session-sentinel", "totp-sentinel",
		"otp-sentinel", "twofa-sentinel", "payment-sentinel", "user-sentinel", "account-sentinel",
	} {
		if strings.Contains(joined, secret) {
			t.Fatalf("prepared evidence leaked %q", secret)
		}
	}
}

func TestPrepareIsDeterministicAndDoesNotMutateInput(t *testing.T) {
	b := validBundle()
	b.Records[0].Metric.Labels[0].Value = "email=person@example.com"
	original := cloneBundle(b)
	one, err := Prepare(b)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Prepare(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.Value(), two.Value()) {
		t.Fatal("redaction is not deterministic")
	}
	if !reflect.DeepEqual(b, original) {
		t.Fatal("Prepare mutated caller input")
	}
}

func TestPrepareFailsClosedForOversizedAndHighCardinalityEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Bundle)
	}{
		{"oversized string", func(b *Bundle) { b.Records[0].Metric.Labels[0].Value = strings.Repeat("x", MaxStringLength+1) }},
		{"high cardinality labels", func(b *Bundle) {
			labels := make([]Label, MaxLabelsPerMetric+1)
			for i := range labels {
				labels[i] = Label{Name: "service", Value: "api"}
			}
			b.Records[0].Metric.Labels = labels
		}},
		{"high cardinality records", func(b *Bundle) { b.Records = make([]Reference, MaxRecords+1) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := validBundle()
			tt.mutate(&b)
			if _, err := Prepare(b); err == nil {
				t.Fatal("Prepare unexpectedly accepted oversized evidence")
			}
		})
	}
}

func TestPrepareNeverEchoesRejectedSecretInError(t *testing.T) {
	b := validBundle()
	secret := "password=do-not-echo"
	b.Version = secret
	_, err := Prepare(b)
	if err == nil {
		t.Fatal("Prepare unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "do-not-echo") {
		t.Fatalf("error leaked rejected secret: %v", err)
	}
}

// RedactText is the pre-send filter for Telegram message bodies. Telegram
// cloud chats are not end-to-end encrypted, so free text must clear the same
// patterns as an evidence bundle before it leaves the process.
func TestRedactTextScrubsSecretsFromFreeText(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		secrets []string
	}{
		{
			name:    "bot token in an assignment",
			input:   "poller failed: bot token=" + fakeBotToken() + " rejected",
			secrets: []string{fakeBotToken(), strings.SplitN(fakeBotToken(), ":", 2)[1]},
		},
		{
			name:    "bot token behind a token label",
			input:   "retrying with token: " + fakeBotToken(),
			secrets: []string{fakeBotToken(), strings.SplitN(fakeBotToken(), ":", 2)[1]},
		},
		{
			name:    "bot token in an authorization header dump",
			input:   "upstream said Bearer 8123456789AAfakebottokenvalue000000 was rejected",
			secrets: []string{"8123456789AAfakebottokenvalue000000"},
		},
		{
			name:    "postgres connection string",
			input:   "dial failed for postgres://ops:hunter2@db.internal:5432/sokalens?sslmode=require after 3 tries",
			secrets: []string{"postgres://", "hunter2", "db.internal"},
		},
		{
			name:    "postgresql connection string",
			input:   "postgresql://admin:s3cr3t@10.0.0.5/sokalens is unreachable",
			secrets: []string{"postgresql://", "s3cr3t"},
		},
		{
			name:    "jwt",
			input:   "session rejected: " + fakeJWT(),
			secrets: []string{jwtHeader(), jwtSignature()},
		},
		{
			name:    "email address",
			input:   "paging on-call person@example.com about the incident",
			secrets: []string{"person@example.com", "example.com"},
		},
		{
			name:    "provider api key",
			input:   "resend rejected re_abcdefghijklmnopqrstuvwxyz with a 401",
			secrets: []string{"re_abcdefghijklmnopqrstuvwxyz"},
		},
		{
			name:    "card number",
			input:   "refund attempted for 4111 1111 1111 1111",
			secrets: []string{"4111"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactText(tt.input)
			for _, secret := range tt.secrets {
				if strings.Contains(got, secret) {
					t.Fatalf("RedactText leaked %q: %q", secret, got)
				}
			}
			if !strings.Contains(got, Redacted) {
				t.Fatalf("RedactText(%q) = %q, want a %q marker", tt.input, got, Redacted)
			}
		})
	}
}

// One message can carry several secrets at once; all must go.
func TestRedactTextScrubsEverySecretInOneBody(t *testing.T) {
	body := strings.Join([]string{
		"incident #142 escalation",
		"db: postgres://ops:hunter2@db.internal:5432/sokalens",
		"token=" + fakeBotToken(),
		"jwt " + fakeJWT(),
		"owner person@example.com",
	}, "\n")
	got := RedactText(body)
	for _, secret := range []string{
		"hunter2", "postgres://", strings.SplitN(fakeBotToken(), ":", 2)[1],
		jwtHeader(), "person@example.com",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("RedactText leaked %q from a multi-secret body: %q", secret, got)
		}
	}
	if !strings.Contains(got, "incident #142 escalation") {
		t.Fatalf("RedactText destroyed non-sensitive text: %q", got)
	}
}

// RedactText must be a pure alias for the shared pattern set, not a fork.
func TestRedactTextMatchesInternalRedact(t *testing.T) {
	for _, input := range []string{
		"",
		"nothing sensitive here",
		"password=hunter2",
		"postgres://ops:hunter2@db.internal/sokalens",
		"Bearer abcdefghijklmnopqrstuvwxyz",
		"  leading and trailing  ",
	} {
		if got, want := RedactText(input), redact(input); got != want {
			t.Fatalf("RedactText(%q) = %q, redact = %q; the pattern list has forked", input, got, want)
		}
	}
}

// Ordinary operational text must survive: over-redaction makes the bridge
// useless and pushes operators back to an unfiltered channel.
func TestRedactTextPreservesOrdinaryOperationalText(t *testing.T) {
	for _, input := range []string{
		"3 open incidents: 2 warning, 1 critical",
		"deploy 9f2c1ab rolled back after 4 failed health checks",
		"queue depth 128, oldest job 42s",
	} {
		if got := RedactText(input); got != input {
			t.Fatalf("RedactText(%q) = %q, want it unchanged", input, got)
		}
	}
}

// TestRedactTextRemovesTelegramBotTokens guards the leak path found during the
// Telegram bridge build: three of these four forms passed through untouched,
// including a bare token and the exact env-var name this service uses. Spec
// §18 forbids a credential reaching a chat under any circumstances, and
// Telegram cloud chats are not end-to-end encrypted, so a leak here is
// recoverable by anyone with account access.
func TestRedactTextRemovesTelegramBotTokens(t *testing.T) {
	// Synthetic and assembled at run time. Never put a real credential in a
	// tracked test file, and never commit one shaped like a real one either.
	secret := fakeBotToken()
	cases := map[string]string{
		"bare token":        secret,
		"service env name":  "OPS_TELEGRAM_BOT_TOKEN=" + secret,
		"underscored key":   "bot_token=" + secret,
		"plain key":         "token=" + secret,
		"inside a sentence": "poller failed using " + secret + " at boot",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got := RedactText(input)
			if strings.Contains(got, secret) {
				t.Fatalf("full token survived redaction: %q", got)
			}
			// The secret half alone is equally damaging.
			if strings.Contains(got, strings.SplitN(secret, ":", 2)[1]) {
				t.Fatalf("token secret survived redaction: %q", got)
			}
		})
	}
}

// fakeBotToken builds a Telegram-shaped bot token at run time.
//
// The literal must never appear in source. gitleaks matches this shape, not
// its meaning, so even an obviously fake value fails the repo's secret-scan
// gate — and that gate is not something to weaken for a test fixture. This
// follows the convention already recorded in .gitleaks.toml, where the ACCA
// cursor fixture was fixed by constructing the value rather than allowlisting
// it. Assembling it here keeps the test honest and the scan green.
func fakeBotToken() string {
	return "8123456789" + ":" + "AA" + strings.Repeat("z", 34)
}

// fakeJWT builds a JWT-shaped fixture at run time.
//
// A contiguous three-segment literal matches the secret scanner's jwt rule
// even when every segment is nonsense. Assembling it keeps the fixture honest
// and the gate sharp, which is the same reason .gitleaks.toml prefers
// constructed fixtures over allowlist entries.
func fakeJWT() string {
	return jwtHeader() + "." + "eyJzdWIiOiIxMjM0NSJ9" + "." + jwtSignature()
}

func jwtHeader() string    { return "eyJ" + "hbGciOiJIUzI1NiJ9" }
func jwtSignature() string { return "dBjftJeZ4CVPmB92K27uhbUJU1p1r" + "_wW1gFWFOEjXk" }
