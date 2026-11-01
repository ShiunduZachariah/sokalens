package evidence

import (
	"errors"
	"regexp"
	"strings"
)

const Redacted = "redacted"

var (
	emailPattern       = regexp.MustCompile(`(?i)\b[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\b`)
	jwtPattern         = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
	authPattern        = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]{6,}`)
	connectionPattern  = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|redis|rediss|mysql|mongodb(?:\+srv)?)://[^\s]+`)
	// The optional (?:[a-z0-9]+[_-])* prefix matters: without it \btoken only
	// matches a bare "token=", so real-world key names carrying an underscore
	// -- OPS_TELEGRAM_BOT_TOKEN=, bot_token=, refresh_token= -- slipped
	// through, because the character before TOKEN is a word character and
	// therefore not a word boundary.
	assignmentPattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9]+[_-])*(?:password|passwd|pwd|token|api[_-]?key|secret|cookie|authorization|session|totp|otp|2fa|payment[_-]?(?:id|token)|card[_-]?(?:number|token)|email|user[_-]?id|account[_-]?id)\s*[:=]\s*[^\s,;]+`)
	// A Telegram bot token is "<bot_id>:<secret>" and carries no key name at
	// all when pasted on its own, so no assignment pattern can catch it. It is
	// matched on shape instead. This is the credential this very service
	// holds, and §18 forbids it reaching a chat under any circumstances.
	telegramBotTokenPattern = regexp.MustCompile(`\b\d{5,16}:[A-Za-z0-9_-]{30,}\b`)
	providerKeyPattern = regexp.MustCompile(`\b(?:sk|re)_[A-Za-z0-9_-]{12,}\b`)
	cardPattern        = regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`)
	sensitiveLabel     = regexp.MustCompile(`(?i)(?:pass(?:word|wd)?|pwd|token|api.?key|secret|cookie|auth(?:orization)?|session|totp|otp|2fa|email|user.?id|account.?id|payment|card)`)
)

// Prepare returns the only type intended for persistence or AI dispatch. It
// clones its input, redacts every string-bearing field, and validates all
// structural/cardinality bounds. Validation errors never include field values.
func Prepare(input Bundle) (PreparedBundle, error) {
	if len(input.Records) == 0 || len(input.Records) > MaxRecords {
		return PreparedBundle{}, errors.New("evidence record count is outside bounds")
	}
	out := cloneBundle(input)
	redactBundle(&out)
	if err := out.Validate(); err != nil {
		return PreparedBundle{}, err
	}
	return PreparedBundle{bundle: out}, nil
}

func redactBundle(bundle *Bundle) {
	bundle.IncidentID = redact(bundle.IncidentID)
	for i := range bundle.Records {
		r := &bundle.Records[i]
		r.ID = redact(r.ID)
		if r.Metric != nil {
			r.Metric.Name = redact(r.Metric.Name)
			r.Metric.Unit = redact(r.Metric.Unit)
			r.Metric.Comparator = redact(r.Metric.Comparator)
			for j := range r.Metric.Labels {
				label := &r.Metric.Labels[j]
				label.Name = redact(label.Name)
				if sensitiveLabel.MatchString(label.Name) {
					label.Value = Redacted
				} else {
					label.Value = redact(label.Value)
				}
			}
		}
		if r.Log != nil {
			r.Log.Stream = redact(r.Log.Stream)
			r.Log.QueryFingerprint = redact(r.Log.QueryFingerprint)
			for j := range r.Log.Levels {
				r.Log.Levels[j].Level = redact(r.Log.Levels[j].Level)
			}
			for j := range r.Log.SampleHashes {
				r.Log.SampleHashes[j] = redact(r.Log.SampleHashes[j])
			}
		}
		if r.Trace != nil {
			r.Trace.TraceIDHash = redact(r.Trace.TraceIDHash)
			r.Trace.Service = redact(r.Trace.Service)
			r.Trace.Operation = redact(r.Trace.Operation)
			r.Trace.Status = redact(r.Trace.Status)
			r.Trace.ErrorClass = redact(r.Trace.ErrorClass)
		}
		if r.PostgreSQL != nil {
			r.PostgreSQL.DatabaseAlias = redact(r.PostgreSQL.DatabaseAlias)
			r.PostgreSQL.QueryFingerprint = redact(r.PostgreSQL.QueryFingerprint)
			r.PostgreSQL.WaitEvent = redact(r.PostgreSQL.WaitEvent)
			for j := range r.PostgreSQL.RelationAliases {
				r.PostgreSQL.RelationAliases[j] = redact(r.PostgreSQL.RelationAliases[j])
			}
		}
		if r.Redis != nil {
			r.Redis.KeyspaceAlias = redact(r.Redis.KeyspaceAlias)
			r.Redis.CommandFamily = redact(r.Redis.CommandFamily)
			r.Redis.ErrorClass = redact(r.Redis.ErrorClass)
		}
		if r.Deployment != nil {
			r.Deployment.Service = redact(r.Deployment.Service)
			r.Deployment.Environment = redact(r.Deployment.Environment)
			r.Deployment.ReleaseID = redact(r.Deployment.ReleaseID)
			r.Deployment.CommitSHA = redact(r.Deployment.CommitSHA)
			r.Deployment.ImageDigest = redact(r.Deployment.ImageDigest)
			r.Deployment.Status = redact(r.Deployment.Status)
		}
	}
}

// RedactText applies the same pattern set as Prepare to a single free-text
// string, for callers that emit text outside an evidence Bundle.
//
// The Telegram ops bridge is the first such caller: Telegram cloud chats are
// not end-to-end encrypted, so every message body crosses the same trust
// boundary as an AI dispatch and must clear the same patterns. This is a
// deliberate reuse of redact rather than a second pattern list — a forked list
// would drift, and the fork that drifts is the one that leaks.
func RedactText(value string) string {
	return redact(value)
}

func redact(value string) string {
	result := value
	for _, pattern := range []*regexp.Regexp{connectionPattern, jwtPattern, authPattern, assignmentPattern, telegramBotTokenPattern, providerKeyPattern, emailPattern, cardPattern} {
		result = pattern.ReplaceAllString(result, Redacted)
	}
	return strings.TrimSpace(result)
}
