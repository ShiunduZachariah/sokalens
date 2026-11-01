package telegram

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Grammar (spec §9):
//
//	message  := "/" command [ "@" botname ] { SP arg }
//	command  := lowercase alpha, no underscores
//	arg      := non-space token | quoted string
//
// Every bound below is enforced on text that arrived from the internet.
const (
	// MaxArgs is the most arguments any command accepts.
	MaxArgs = 4
	// MaxArgTextChars bounds the whole argument region, quotes included.
	MaxArgTextChars = 256
	// MaxCommandChars bounds the command word.
	MaxCommandChars = 32
	// MaxBotNameChars bounds the "@botname" suffix.
	MaxBotNameChars = 64
	// MaxTextBytes is a coarse guard so a malformed update never makes the
	// parser walk megabytes. Telegram caps a message at 4096 characters,
	// which is at most four bytes each.
	MaxTextBytes = 4 * 4096
)

// Outcome labels handed to Metrics.ObserveCommand.
const (
	StatusOK       = "ok"
	StatusError    = "error"
	StatusPanic    = "panic"
	StatusUnknown  = "unknown"
	StatusRejected = "rejected"
)

// Command labels for updates that never reached a handler. The parsed name is
// attacker-controlled, so it is only ever used as a metric label after the
// registry has confirmed it is one of ours; otherwise these fixed labels keep
// the metric's cardinality bounded.
const (
	commandLabelUnknown = "unknown"
	commandLabelInvalid = "invalid"
)

var (
	// ErrNotCommand means the text is ordinary chat. Ops channels carry
	// conversation; the bridge stays silent rather than answering it.
	ErrNotCommand = errors.New("telegram: message is not a command")
	// ErrInvalidCommand means the text starts with "/" but the word after it
	// is not a command in the grammar.
	ErrInvalidCommand = errors.New("telegram: command name is not valid")
	// ErrTextTooLong means the raw message exceeded MaxTextBytes.
	ErrTextTooLong = errors.New("telegram: message text is too long")
	// ErrTooManyArgs means more than MaxArgs arguments were supplied.
	ErrTooManyArgs = errors.New("telegram: too many arguments")
	// ErrArgTextTooLong means the argument region exceeded MaxArgTextChars.
	ErrArgTextTooLong = errors.New("telegram: argument text is too long")
	// ErrUnsafeArgument means an argument carried a shell metacharacter or a
	// control character.
	ErrUnsafeArgument = errors.New("telegram: argument contains a forbidden character")
	// ErrMalformedQuote means a quoted argument was unterminated or ran into
	// another token.
	ErrMalformedQuote = errors.New("telegram: malformed quoted argument")

	// ErrDuplicateCommand is returned by Registry.Register.
	ErrDuplicateCommand = errors.New("telegram: command already registered")
	// ErrInvalidRegistration is returned by Registry.Register.
	ErrInvalidRegistration = errors.New("telegram: invalid command registration")
	// ErrHandlerPanic wraps a panic recovered inside a command handler. The
	// poller runs one goroutine; a handler panic must not take it down.
	ErrHandlerPanic = errors.New("telegram: handler panicked")
)

// Command is a parsed message. Args preserve their original case; only the
// command word is normalized.
type Command struct {
	Name string
	// Bot is the "@botname" suffix Telegram appends in groups, without the
	// "@", or empty when the message carried none.
	Bot  string
	Args []string
}

// ParseCommand applies the grammar to raw message text.
//
// Errors are deliberately specific but carry no part of the input: an error
// string can end up in a reply, and echoing an attacker's bytes back into an
// HTML message is exactly the class of bug Esc exists to prevent.
func ParseCommand(text string) (Command, error) {
	if len(text) > MaxTextBytes {
		return Command{}, ErrTextTooLong
	}
	// Trailing whitespace is a typing artefact. Whitespace anywhere else is
	// either a separator or, for control characters, a rejection below.
	trimmed := strings.TrimRight(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "/") {
		return Command{}, ErrNotCommand
	}

	body := trimmed[1:]
	head, rest := body, ""
	if i := strings.IndexByte(body, ' '); i >= 0 {
		head, rest = body[:i], body[i+1:]
	}

	name, bot, err := splitCommandWord(head)
	if err != nil {
		return Command{}, err
	}
	args, err := parseArgs(rest)
	if err != nil {
		return Command{}, err
	}
	return Command{Name: name, Bot: bot, Args: args}, nil
}

// PeekCommand is the never-failing form of ParseCommand. The poller calls it
// before authorization, to name the command in the forensic ledger and to build
// the Request it hands the dispatcher.
//
// Any rejection collapses to ("", nil). Returning the command word without its
// arguments would be worse than returning nothing: "/deploy web;rm" would
// arrive at the dispatcher as a bare "/deploy", which is a different command
// with different consequences. The raw text is recorded separately, so
// discarding the name here costs the ledger nothing.
func PeekCommand(text string) (string, []string) {
	cmd, err := ParseCommand(text)
	if err != nil {
		return "", nil
	}
	return cmd.Name, cmd.Args
}

// splitCommandWord strips the "@botname" Telegram appends in group chats and
// normalizes the command word.
//
// Case folding is ASCII-only and applied after validation. strings.ToLower
// would fold U+212A KELVIN SIGN to "k", so "/statusK" with a Kelvin sign would
// otherwise parse as a different byte string that still matches a registered
// name.
func splitCommandWord(head string) (name, bot string, err error) {
	name = head
	if i := strings.IndexByte(head, '@'); i >= 0 {
		name, bot = head[:i], head[i+1:]
		if !validBotName(bot) {
			return "", "", ErrInvalidCommand
		}
	}
	if name == "" || len(name) > MaxCommandChars {
		return "", "", ErrInvalidCommand
	}
	lowered := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z':
			lowered[i] = c
		case c >= 'A' && c <= 'Z':
			lowered[i] = c + ('a' - 'A')
		default:
			// Digits, underscores and every non-ASCII byte are out: the
			// grammar says lowercase alpha, no underscores.
			return "", "", ErrInvalidCommand
		}
	}
	return string(lowered), bot, nil
}

func validBotName(bot string) bool {
	if bot == "" || len(bot) > MaxBotNameChars {
		return false
	}
	for i := 0; i < len(bot); i++ {
		c := bot[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// parseArgs tokenizes the argument region. The region is bounded before any
// tokenizing happens, so an over-long message is rejected without the parser
// walking it twice.
func parseArgs(rest string) ([]string, error) {
	region := strings.Trim(rest, " ")
	if region == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(region) > MaxArgTextChars {
		return nil, ErrArgTextTooLong
	}

	var args []string
	for i := 0; i < len(region); {
		if region[i] == ' ' {
			i++
			continue
		}
		var token string
		if region[i] == '"' {
			j := strings.IndexByte(region[i+1:], '"')
			if j < 0 {
				return nil, ErrMalformedQuote
			}
			token = region[i+1 : i+1+j]
			i += j + 2
			if i < len(region) && region[i] != ' ' {
				return nil, ErrMalformedQuote // "a"b — ambiguous, not accepted
			}
		} else {
			j := strings.IndexByte(region[i:], ' ')
			if j < 0 {
				token, i = region[i:], len(region)
			} else {
				token, i = region[i:i+j], i+j
			}
			if strings.Contains(token, `"`) {
				return nil, ErrMalformedQuote
			}
		}
		if len(args) >= MaxArgs {
			return nil, ErrTooManyArgs
		}
		if err := checkArg(token); err != nil {
			return nil, err
		}
		args = append(args, token)
	}
	return args, nil
}

// checkArg rejects the characters that have no legitimate use in this grammar.
// The command surface never shells out, so this is defence in depth rather than
// the only defence — but an argument reaching a future runbook, a SQL LIKE, or
// a log line is one place a metacharacter must never arrive.
func checkArg(arg string) error {
	if !utf8.ValidString(arg) {
		return ErrUnsafeArgument
	}
	if strings.Contains(arg, "$(") {
		return ErrUnsafeArgument
	}
	for _, r := range arg {
		switch r {
		case ';', '|', '&', '`':
			return ErrUnsafeArgument
		}
		// Newline is the character the spec names; rejecting the whole
		// control range costs nothing and closes the rest of the family.
		if r < 0x20 || r == 0x7f {
			return ErrUnsafeArgument
		}
	}
	return nil
}

// Registry holds the dispatchable commands. /help renders from it, so the help
// text cannot drift from what is actually dispatchable.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Registration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]Registration)}
}

const (
	maxUsageChars   = 64
	maxSummaryChars = 120
)

// Register adds one command. The name may be written with or without its
// leading slash. Registration is a boot-time operation and every failure is a
// programming error, so they are returned rather than ignored.
func (r *Registry) Register(reg Registration) error {
	name, _, err := splitCommandWord(strings.TrimPrefix(reg.Name, "/"))
	if err != nil {
		return fmt.Errorf("%w: name", ErrInvalidRegistration)
	}
	if reg.Handler == nil {
		return fmt.Errorf("%w: handler is nil", ErrInvalidRegistration)
	}
	if utf8.RuneCountInString(reg.Usage) > maxUsageChars {
		return fmt.Errorf("%w: usage is too long", ErrInvalidRegistration)
	}
	if strings.TrimSpace(reg.Summary) == "" || utf8.RuneCountInString(reg.Summary) > maxSummaryChars {
		return fmt.Errorf("%w: summary", ErrInvalidRegistration)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.entries[name]; exists {
		return fmt.Errorf("%w: /%s", ErrDuplicateCommand, name)
	}
	reg.Name = name
	r.entries[name] = reg
	return nil
}

// MustRegister panics on a registration error. Boot-time use only.
func (r *Registry) MustRegister(reg Registration) {
	if err := r.Register(reg); err != nil {
		panic(err)
	}
}

// Lookup finds a registration by normalized name.
func (r *Registry) Lookup(name string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.entries[name]
	return reg, ok
}

// List returns the registrations sorted by name.
func (r *Registry) List() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Registration, 0, len(r.entries))
	for _, reg := range r.entries {
		out = append(out, reg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Help renders the /help body from the registry. It is the reply to /help and
// to any unknown command: guessing what an operator meant is how the wrong
// service gets restarted.
func (r *Registry) Help() string {
	regs := r.List()
	if len(regs) == 0 {
		return Bold("No commands are registered.")
	}
	rows := make([][]string, 0, len(regs))
	mutating := false
	for _, reg := range regs {
		left := "/" + reg.Name
		if reg.Usage != "" {
			left += " " + reg.Usage
		}
		if reg.Mutating {
			left += " *"
			mutating = true
		}
		rows = append(rows, []string{left, reg.Summary})
	}
	body := Bold("Commands") + "\n" + PreTable(rows)
	if mutating {
		body += "\n* changes state."
	}
	return body
}

// Router dispatches a parsed command to a registered handler. It satisfies the
// poller's Dispatcher interface.
type Router struct {
	registry *Registry
	metrics  Metrics
}

var _ Dispatcher = (*Router)(nil)

// NewRouter builds a router. A nil Metrics is accepted and discarded.
func NewRouter(registry *Registry, metrics Metrics) *Router {
	if registry == nil {
		registry = NewRegistry()
	}
	if metrics == nil {
		metrics = discardMetrics{}
	}
	return &Router{registry: registry, metrics: metrics}
}

// Registry exposes the router's registry, so a /help handler can render it.
func (r *Router) Registry() *Registry { return r.registry }

// Dispatch runs the handler for an already-authorized, already-parsed command
// and records the outcome.
//
// An empty command — what PeekCommand returns for ordinary chat — is answered
// with Response{Silent: true}. An ops channel is a conversation, and a bot that
// says "I don't understand" in it is noise that trains people to mute it.
//
// An unknown command is answered with the /help body, rendered from the
// registry. The router never fuzzy-matches: guessing what an operator meant is
// how the wrong service gets restarted.
//
// The request is re-validated here even though PeekCommand already validated
// it. The router is the last gate before a handler, and it is cheap for it not
// to trust its caller.
func (r *Router) Dispatch(ctx context.Context, req Request) (Response, error) {
	name, _, err := splitCommandWord(strings.TrimPrefix(req.Command, "/"))
	if err != nil || req.Command == "" {
		return Response{Silent: true}, nil
	}
	if err := validateArgs(req.Args); err != nil {
		r.metrics.ObserveCommand(commandLabelInvalid, StatusRejected, 0)
		// Say which rule was broken, without quoting back a single byte of
		// what was sent.
		return Response{Body: rejectionBody(err)}, nil
	}

	reg, ok := r.registry.Lookup(name)
	if !ok {
		r.metrics.ObserveCommand(commandLabelUnknown, StatusUnknown, 0)
		return Response{Body: r.registry.Help()}, nil
	}

	req.Command = name
	req.ReplyToText = TruncateLog(req.ReplyToText)

	start := time.Now()
	resp, err := execute(ctx, reg.Handler, req)
	elapsed := time.Since(start)
	switch {
	case err == nil:
		r.metrics.ObserveCommand(name, StatusOK, elapsed)
		return resp, nil
	case errors.Is(err, ErrHandlerPanic):
		r.metrics.ObserveCommand(name, StatusPanic, elapsed)
		return Response{}, err
	default:
		r.metrics.ObserveCommand(name, StatusError, elapsed)
		return Response{}, err
	}
}

// validateArgs re-applies the argument bounds to an already-parsed request.
func validateArgs(args []string) error {
	if len(args) > MaxArgs {
		return ErrTooManyArgs
	}
	total := 0
	for _, arg := range args {
		if err := checkArg(arg); err != nil {
			return err
		}
		total += utf8.RuneCountInString(arg) + 1
	}
	if total > MaxArgTextChars {
		return ErrArgTextTooLong
	}
	return nil
}

// execute isolates a handler panic. The recovered value goes into the error the
// caller logs, never into a Response: it can quote the input that caused it.
func execute(ctx context.Context, h Handler, req Request) (resp Response, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			resp = Response{}
			err = fmt.Errorf("%w: %v", ErrHandlerPanic, rec)
		}
	}()
	if h == nil {
		return Response{}, fmt.Errorf("%w: no handler", ErrInvalidRegistration)
	}
	return h.Execute(ctx, req)
}

// rejectionBody maps a parse rejection to a fixed reply. Each string is
// constant: nothing from the message is interpolated, so nothing needs escaping
// and nothing can inject markup.
func rejectionBody(err error) string {
	switch {
	case errors.Is(err, ErrTooManyArgs):
		return "Rejected: too many arguments (maximum 4)."
	case errors.Is(err, ErrArgTextTooLong):
		return "Rejected: argument text is too long (maximum 256 characters)."
	case errors.Is(err, ErrUnsafeArgument):
		return "Rejected: an argument contains a character that is not allowed."
	case errors.Is(err, ErrMalformedQuote):
		return "Rejected: unbalanced quotes."
	default:
		return "Rejected: the command could not be parsed."
	}
}

// discardMetrics satisfies Metrics for callers that have not wired a collector.
type discardMetrics struct{}

func (discardMetrics) SetPollerUp(bool)                             {}
func (discardMetrics) SetOffset(int64)                              {}
func (discardMetrics) IncUpdatesReceived()                          {}
func (discardMetrics) IncUnauthorized()                             {}
func (discardMetrics) IncDuplicate()                                {}
func (discardMetrics) IncPollError(string)                          {}
func (discardMetrics) ObserveCommand(string, string, time.Duration) {}
func (discardMetrics) IncMessageSent(string)                        {}
func (discardMetrics) IncMessageFailed(string)                      {}
func (discardMetrics) IncMessageSuppressed()                        {}
