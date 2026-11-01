package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// observedCommand is one ObserveCommand call.
type observedCommand struct {
	command string
	status  string
	elapsed time.Duration
}

// stubMetrics records the dispatch observations the router makes.
type stubMetrics struct {
	discardMetrics
	mu       sync.Mutex
	observed []observedCommand
}

func (m *stubMetrics) ObserveCommand(command, status string, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observed = append(m.observed, observedCommand{command, status, d})
}

func (m *stubMetrics) calls() []observedCommand {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]observedCommand(nil), m.observed...)
}

func okHandler(body string) Handler {
	return HandlerFunc(func(context.Context, Request) (Response, error) {
		return Response{Body: body}, nil
	})
}

// testRegistry is a small registry standing in for the real command set.
func testRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry()
	for _, r := range []Registration{
		{Name: "status", Summary: "platform health at a glance", Handler: okHandler("status ok")},
		{Name: "incident", Usage: "<id>", Summary: "one incident in detail", Handler: okHandler("incident")},
		{Name: "deploys", Summary: "recent deployments", Handler: okHandler("deploys")},
		{Name: "hold", Usage: "<id>", Summary: "hold the release train", Mutating: true, Handler: okHandler("held")},
	} {
		if err := reg.Register(r); err != nil {
			t.Fatalf("Register(%s): %v", r.Name, err)
		}
	}
	return reg
}

// dispatchText walks the path the poller walks: peek the raw text, build the
// request from it, dispatch.
func dispatchText(t *testing.T, r *Router, text string) Response {
	t.Helper()
	name, args := PeekCommand(text)
	resp, err := r.Dispatch(context.Background(), Request{
		Command: name, Args: args, UserID: ownerID, ChatID: authChatID, UpdateID: 42,
	})
	if err != nil {
		t.Fatalf("Dispatch(%q): %v", text, err)
	}
	return resp
}

func TestParseValidCommands(t *testing.T) {
	tests := []struct {
		text string
		name string
		args []string
	}{
		{"/status", "status", nil},
		{"/incident abc123", "incident", []string{"abc123"}},
		{"/deploys web staging", "deploys", []string{"web", "staging"}},
		{"/incident   spaced   out  ", "incident", []string{"spaced", "out"}},
		{"/status a b c d", "status", []string{"a", "b", "c", "d"}}, // exactly MaxArgs
	}
	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			cmd, err := ParseCommand(tc.text)
			if err != nil {
				t.Fatalf("ParseCommand: %v", err)
			}
			if cmd.Name != tc.name {
				t.Fatalf("Name = %q, want %q", cmd.Name, tc.name)
			}
			if strings.Join(cmd.Args, "\x00") != strings.Join(tc.args, "\x00") {
				t.Fatalf("Args = %q, want %q", cmd.Args, tc.args)
			}
		})
	}
}

func TestParseStripsBotSuffix(t *testing.T) {
	// Telegram appends @botname to every command typed in a group.
	cmd, err := ParseCommand("/status@sokalens_ops_bot alpha")
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if cmd.Name != "status" {
		t.Fatalf("Name = %q, want %q", cmd.Name, "status")
	}
	if cmd.Bot != "sokalens_ops_bot" {
		t.Fatalf("Bot = %q, want the suffix preserved for the caller", cmd.Bot)
	}
	if len(cmd.Args) != 1 || cmd.Args[0] != "alpha" {
		t.Fatalf("Args = %q", cmd.Args)
	}

	// A well-formed suffix followed by arguments still parses; only the
	// suffix itself has to be a legal bot name.
	if cmd, err := ParseCommand("/status@sokalens_bot arg"); err != nil || cmd.Bot != "sokalens_bot" {
		t.Fatalf("ParseCommand = (%+v, %v)", cmd, err)
	}
	for _, bad := range []string{"/status@", "/status@bad!name", "/status@bad-name"} {
		if _, err := ParseCommand(bad); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("ParseCommand(%q) err = %v, want ErrInvalidCommand", bad, err)
		}
	}
}

func TestCommandMatchIsCaseInsensitiveAndArgumentsKeepTheirCase(t *testing.T) {
	cmd, err := ParseCommand("/INCIDENT AbC-123")
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	if cmd.Name != "incident" {
		t.Fatalf("Name = %q, want %q", cmd.Name, "incident")
	}
	// UUIDs, branch names and service names are case-sensitive downstream.
	if cmd.Args[0] != "AbC-123" {
		t.Fatalf("Args[0] = %q, want the original case preserved", cmd.Args[0])
	}

	// Case folding is ASCII-only: U+212A KELVIN SIGN lowercases to "k" under
	// strings.ToLower and must not become part of a command name.
	if _, err := ParseCommand("/\u212Aueues"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("Kelvin sign err = %v, want ErrInvalidCommand", err)
	}
}

func TestParseQuotedArguments(t *testing.T) {
	cmd, err := ParseCommand(`/incident "checkout latency spike" web`)
	if err != nil {
		t.Fatalf("ParseCommand: %v", err)
	}
	want := []string{"checkout latency spike", "web"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Fatalf("Args = %q, want %q", cmd.Args, want)
	}

	for _, bad := range []string{
		`/incident "unterminated`,
		`/incident "a"b`,
		`/incident tra"iling`,
	} {
		if _, err := ParseCommand(bad); !errors.Is(err, ErrMalformedQuote) {
			t.Fatalf("ParseCommand(%q) err = %v, want ErrMalformedQuote", bad, err)
		}
	}
}

func TestParseRejectsAFifthArgument(t *testing.T) {
	if _, err := ParseCommand("/status a b c d e"); !errors.Is(err, ErrTooManyArgs) {
		t.Fatalf("err = %v, want ErrTooManyArgs", err)
	}
}

func TestParseArgumentTextLengthBoundary(t *testing.T) {
	// Exactly MaxArgTextChars of argument text is accepted; one more is not.
	at := "/status " + strings.Repeat("x", MaxArgTextChars)
	if _, err := ParseCommand(at); err != nil {
		t.Fatalf("%d characters of argument text: %v", MaxArgTextChars, err)
	}
	over := "/status " + strings.Repeat("x", MaxArgTextChars+1)
	if _, err := ParseCommand(over); !errors.Is(err, ErrArgTextTooLong) {
		t.Fatalf("%d characters: err = %v, want ErrArgTextTooLong", MaxArgTextChars+1, err)
	}
}

func TestParseRejectsEachInjectionCharacter(t *testing.T) {
	// Each metacharacter is rejected on its own, so no test passes only
	// because a neighbouring rule happened to fire.
	tests := []struct {
		name string
		text string
	}{
		{"semicolon", "/incident abc;whoami"},
		{"pipe", "/incident abc|whoami"},
		{"ampersand", "/incident abc&whoami"},
		{"backtick", "/incident abc`whoami`"},
		{"command substitution", "/incident abc$(whoami)"},
		{"newline", "/incident abc\ndef"},
		{"carriage return", "/incident abc\rdef"},
		{"tab", "/incident \"abc\tdef\""},
		{"nul", "/incident abc\x00def"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCommand(tc.text); !errors.Is(err, ErrUnsafeArgument) {
				t.Fatalf("err = %v, want ErrUnsafeArgument", err)
			}
		})
	}

	// The same characters are rejected inside quotes, where a naive
	// tokenizer would let them through.
	if _, err := ParseCommand(`/incident "abc; rm -rf"`); !errors.Is(err, ErrUnsafeArgument) {
		t.Fatalf("quoted metacharacter err = %v, want ErrUnsafeArgument", err)
	}

	// A lone "$" is not command substitution and stays usable.
	if _, err := ParseCommand("/incident price$"); err != nil {
		t.Fatalf("lone dollar sign: %v", err)
	}
}

func TestParseRejectsNonCommands(t *testing.T) {
	for _, text := range []string{"", "hello team", "the /status looks bad", " /status"} {
		if _, err := ParseCommand(text); !errors.Is(err, ErrNotCommand) {
			t.Fatalf("ParseCommand(%q) err = %v, want ErrNotCommand", text, err)
		}
	}
	for _, text := range []string{"/", "//status", "/st4tus", "/dead_letters", "/статус"} {
		if _, err := ParseCommand(text); !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("ParseCommand(%q) err = %v, want ErrInvalidCommand", text, err)
		}
	}
	if _, err := ParseCommand("/status " + strings.Repeat("x", MaxTextBytes)); !errors.Is(err, ErrTextTooLong) {
		t.Fatalf("oversized text err = %v, want ErrTextTooLong", err)
	}
}

func TestPeekCommandNeverHandsOverACommandStrippedOfItsArguments(t *testing.T) {
	// "/hold web;rm" must not reach the dispatcher as a bare "/hold": that is
	// a different command with different consequences.
	name, args := PeekCommand("/hold web;rm")
	if name != "" || args != nil {
		t.Fatalf("PeekCommand = (%q, %q), want the whole command dropped", name, args)
	}
	if name, args := PeekCommand("/hold web"); name != "hold" || len(args) != 1 {
		t.Fatalf("PeekCommand = (%q, %q), want a clean parse", name, args)
	}
}

func TestPlainTextIsSilent(t *testing.T) {
	metrics := &stubMetrics{}
	r := NewRouter(testRegistry(t), metrics)

	for _, text := range []string{"deploy looks stuck", "", "anyone around?"} {
		resp := dispatchText(t, r, text)
		if !resp.Silent {
			t.Fatalf("text %q got a reply: %q", text, resp.Body)
		}
		if resp.Body != "" {
			t.Fatalf("silent response carried a body: %q", resp.Body)
		}
	}
	if len(metrics.calls()) != 0 {
		t.Fatalf("ordinary chat was recorded as a command: %+v", metrics.calls())
	}
}

func TestUnknownCommandRepliesWithHelp(t *testing.T) {
	metrics := &stubMetrics{}
	registry := testRegistry(t)
	r := NewRouter(registry, metrics)

	resp := dispatchText(t, r, "/statuz")
	if resp.Silent {
		t.Fatal("an unknown command was answered with silence")
	}
	if resp.Body != registry.Help() {
		t.Fatalf("unknown command body is not the help text:\n%s", resp.Body)
	}
	// Never guess: "/statuz" must not run "/status".
	if strings.Contains(resp.Body, "status ok") {
		t.Fatal("an unknown command was fuzzy-matched to a handler")
	}

	calls := metrics.calls()
	if len(calls) != 1 || calls[0].status != StatusUnknown {
		t.Fatalf("metrics = %+v, want one %q observation", calls, StatusUnknown)
	}
	// The label must not carry the attacker-chosen word.
	if calls[0].command != commandLabelUnknown {
		t.Fatalf("metric label = %q, want the fixed %q label", calls[0].command, commandLabelUnknown)
	}
}

func TestDispatchRunsTheHandlerAndRecordsTheOutcome(t *testing.T) {
	metrics := &stubMetrics{}
	registry := NewRegistry()
	if err := registry.Register(Registration{
		Name:    "status",
		Summary: "platform health",
		Handler: HandlerFunc(func(_ context.Context, req Request) (Response, error) {
			if req.Command != "status" || len(req.Args) != 1 || req.Args[0] != "Web" {
				return Response{}, errors.New("request not carried through")
			}
			time.Sleep(time.Millisecond)
			return Response{Body: "ok"}, nil
		}),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r := NewRouter(registry, metrics)
	resp := dispatchText(t, r, "/status Web")
	if resp.Body != "ok" {
		t.Fatalf("Body = %q", resp.Body)
	}

	calls := metrics.calls()
	if len(calls) != 1 {
		t.Fatalf("metrics = %+v, want one observation", calls)
	}
	if calls[0].command != "status" || calls[0].status != StatusOK {
		t.Fatalf("observation = %+v", calls[0])
	}
	if calls[0].elapsed <= 0 {
		t.Fatalf("duration = %v, want a measured duration", calls[0].elapsed)
	}
}

func TestDispatchRecordsHandlerFailuresAndContainsPanics(t *testing.T) {
	sentinel := errors.New("query failed")
	registry := NewRegistry()
	registry.MustRegister(Registration{Name: "boom", Summary: "fails", Handler: HandlerFunc(
		func(context.Context, Request) (Response, error) { return Response{}, sentinel })})
	registry.MustRegister(Registration{Name: "panics", Summary: "panics", Handler: HandlerFunc(
		func(context.Context, Request) (Response, error) { panic("nil map write") })})

	metrics := &stubMetrics{}
	r := NewRouter(registry, metrics)

	if _, err := r.Dispatch(context.Background(), Request{Command: "boom"}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the handler's error", err)
	}
	// A handler panic must not take the poller goroutine with it.
	resp, err := r.Dispatch(context.Background(), Request{Command: "panics"})
	if !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("err = %v, want ErrHandlerPanic", err)
	}
	if resp.Body != "" {
		t.Fatalf("a recovered panic leaked a body: %q", resp.Body)
	}

	calls := metrics.calls()
	if len(calls) != 2 || calls[0].status != StatusError || calls[1].status != StatusPanic {
		t.Fatalf("metrics = %+v", calls)
	}
}

func TestDispatchRevalidatesArgumentsItIsHanded(t *testing.T) {
	metrics := &stubMetrics{}
	r := NewRouter(testRegistry(t), metrics)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"five arguments", []string{"alpha", "bravo", "charlie", "delta", "echo"}, "maximum 4"},
		{"metacharacter", []string{"alpha;bravo"}, "not allowed"},
		{"over-long", []string{strings.Repeat("x", MaxArgTextChars+1)}, "maximum 256"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := r.Dispatch(context.Background(), Request{Command: "status", Args: tc.args})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if !strings.Contains(resp.Body, tc.want) {
				t.Fatalf("Body = %q, want it to mention %q", resp.Body, tc.want)
			}
			// The rejection must not echo the rejected value back into an
			// HTML message.
			for _, arg := range tc.args {
				if arg != "" && strings.Contains(resp.Body, arg) {
					t.Fatalf("rejection echoed the input: %q", resp.Body)
				}
			}
		})
	}
	for _, c := range metrics.calls() {
		if c.command != commandLabelInvalid || c.status != StatusRejected {
			t.Fatalf("observation = %+v", c)
		}
	}
}

func TestDispatchIgnoresAnUnparseableCommandField(t *testing.T) {
	r := NewRouter(testRegistry(t), nil)
	for _, name := range []string{"", "st4tus", "dead_letters", "/"} {
		resp, err := r.Dispatch(context.Background(), Request{Command: name})
		if err != nil {
			t.Fatalf("Dispatch(%q): %v", name, err)
		}
		if !resp.Silent {
			t.Fatalf("Dispatch(%q) replied: %q", name, resp.Body)
		}
	}
}

func TestRegistryValidatesRegistrations(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(Registration{Name: "status", Summary: "ok", Handler: okHandler("x")}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(Registration{Name: "/status", Summary: "ok", Handler: okHandler("x")}); !errors.Is(err, ErrDuplicateCommand) {
		t.Fatalf("duplicate err = %v, want ErrDuplicateCommand", err)
	}
	for _, bad := range []Registration{
		{Name: "bad_name", Summary: "ok", Handler: okHandler("x")},
		{Name: "nohandler", Summary: "ok"},
		{Name: "nosummary", Handler: okHandler("x")},
		{Name: "toolong", Summary: strings.Repeat("s", maxSummaryChars+1), Handler: okHandler("x")},
	} {
		if err := reg.Register(bad); !errors.Is(err, ErrInvalidRegistration) {
			t.Fatalf("Register(%+v) err = %v, want ErrInvalidRegistration", bad.Name, err)
		}
	}
}

func TestHelpRendersFromTheRegistry(t *testing.T) {
	registry := testRegistry(t)
	help := registry.Help()

	// Every dispatchable command appears, so help cannot drift from what the
	// router will actually accept.
	for _, reg := range registry.List() {
		if !strings.Contains(help, "/"+reg.Name) {
			t.Fatalf("help omits /%s:\n%s", reg.Name, help)
		}
		if !strings.Contains(help, Esc(reg.Summary)) {
			t.Fatalf("help omits the summary for /%s", reg.Name)
		}
	}
	// Usage strings contain angle brackets and must arrive escaped.
	if strings.Contains(help, "<id>") {
		t.Fatalf("help contains an unescaped usage placeholder:\n%s", help)
	}
	if !strings.Contains(help, "&lt;id&gt;") {
		t.Fatalf("help lost the usage placeholder:\n%s", help)
	}
	// Mutating commands are marked.
	if !strings.Contains(help, "* changes state.") {
		t.Fatalf("help does not mark mutating commands:\n%s", help)
	}
	// Help must be sendable in one message.
	if parts := Chunk(help); len(parts) != 1 {
		t.Fatalf("help chunked into %d parts", len(parts))
	}

	if empty := NewRegistry().Help(); empty == "" {
		t.Fatal("an empty registry rendered an empty help body")
	}
}

func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	registry := testRegistry(t)
	r := NewRouter(registry, &stubMetrics{})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Dispatch(context.Background(), Request{Command: "status"})
			_ = registry.Help()
		}()
	}
	wg.Wait()
}
