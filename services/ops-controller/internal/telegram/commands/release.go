package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/releases"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// ---------------------------------------------------------------------------
// /release and /hold — the two mutating release-train commands (plan phase T4).
//
// These are the first commands in this package that write. Both write durably
// to Postgres before a reply is produced (spec §7.2, "Postgres before
// Telegram"): if the write fails the operator is told it failed, and there is
// never a reply describing a state change that did not happen.
//
// Neither command deploys. /release promotes the pending train to DEPLOYING
// and records the PR list and SHA range; the existing CI deploy path does the
// deploying. Nothing here shells out or builds a command string.
//
// This file deliberately declares its own ReleaseDeps and RegisterRelease
// rather than extending the read-only Deps in commands.go: the read commands
// are guaranteed side-effect free, and mixing a mutating dependency into their
// struct would quietly remove that guarantee.
// ---------------------------------------------------------------------------

// ReleaseDeps are the mutating release-train dependencies. Either may be nil,
// and a nil source degrades to "unavailable" exactly like the read commands —
// a mutating command that cannot reach its store must say so rather than
// erroring into the poller.
//
// Trains and Confirmations must be backed by the same database: /release
// consumes its confirmation token in the same transaction that promotes the
// train.
type ReleaseDeps struct {
	Trains        *releases.Manager
	Confirmations *releases.Confirmations

	// Location renders windows in the operator's zone (EAT in production).
	Location *time.Location
	// Now is injectable for tests.
	Now func() time.Time
	// QueryTimeout bounds each command so one slow write cannot hold the
	// poller's update-handling path open.
	QueryTimeout time.Duration
}

const (
	// releaseUnavailable is the degraded reply for both commands.
	releaseUnavailable = "Release train " + unavailable + "."
	// defaultReleaseTimeout is longer than the read-command timeout: these
	// commands take an advisory lock and write.
	defaultReleaseTimeout = 10 * time.Second
)

func (d ReleaseDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d ReleaseDeps) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return releases.EAT()
}

func (d ReleaseDeps) scope(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := d.QueryTimeout
	if timeout <= 0 {
		timeout = defaultReleaseTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// ready reports whether both stores are wired.
func (d ReleaseDeps) ready() bool { return d.Trains != nil && d.Confirmations != nil }

// RegisterRelease adds /release and /hold to the registry.
//
// It is separate from Register in commands.go so the read commands and the
// mutating commands can be wired independently in main, and so that a
// deployment which chooses not to expose mutations simply does not call this.
func RegisterRelease(registry *telegram.Registry, deps ReleaseDeps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	all := []telegram.Registration{
		{
			Name:    "release",
			Usage:   "[token]",
			Summary: "Promote the pending release train. Confirmation required.",
			// Mutating: this writes to Postgres before it answers.
			Mutating: true,
			Handler:  telegram.HandlerFunc(deps.release),
		},
		{
			Name:     "hold",
			Usage:    "[reason]",
			Summary:  "Skip the next release window. Auto-expires after it passes.",
			Mutating: true,
			Handler:  telegram.HandlerFunc(deps.hold),
		},
	}
	for _, reg := range all {
		if err := registry.Register(reg); err != nil {
			return fmt.Errorf("register /%s: %w", reg.Name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// /release
// ---------------------------------------------------------------------------

// release is the two-phase promotion. With no argument it mints a confirmation
// token; with a token it redeems it and promotes.
func (d ReleaseDeps) release(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	if !d.ready() {
		return telegram.Response{Body: releaseUnavailable}, nil
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()

	if len(req.Args) == 0 {
		return d.requestRelease(scoped, req)
	}
	return d.confirmRelease(scoped, req)
}

// requestRelease is phase one: describe the train and issue a token.
func (d ReleaseDeps) requestRelease(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	status, err := d.Trains.Status(ctx)
	if err != nil {
		return telegram.Response{Body: releaseUnavailable}, nil
	}
	// An empty train is refused here and again at confirmation time. Promoting
	// nothing would record a release that shipped no change.
	if !status.Exists || len(status.Train.PRNumbers) == 0 {
		return telegram.Response{Body: "Pending train is empty. Nothing to release."}, nil
	}
	if status.Held {
		return telegram.Response{Body: d.heldBody(status)}, nil
	}

	confirmation, err := d.Confirmations.Issue(ctx, releases.RunbookRelease, map[string]any{
		"train_id":   status.Train.ID,
		"pr_numbers": status.Train.PRNumbers,
	}, req.UserID, req.ChatID)
	switch {
	case errors.Is(err, releases.ErrTooManyOutstanding):
		return telegram.Response{Body: "Too many outstanding confirmations. Let them expire before asking again."}, nil
	case err != nil:
		return telegram.Response{Body: releaseUnavailable}, nil
	}

	var b strings.Builder
	b.WriteString(telegram.Bold("Release train #"+strconv.FormatInt(status.Train.ID, 10)) + "\n\n")
	b.WriteString(trainSummary(status.Train) + "\n")
	b.WriteString("Window: " + d.stamp(status.Train.WindowAt) + "\n")
	if status.Due {
		b.WriteString("Due now: " + telegram.Esc(status.DueReason) + "\n")
	}
	if status.SplitRequired {
		b.WriteString("Previous train was rolled back: this one promotes in two halves.\n")
	}
	b.WriteString("\nReply " + telegram.Code("/release "+confirmation.Token) + " within " +
		telegram.Esc(shortDuration(d.Confirmations.TTL())) + " to promote it.\n")
	b.WriteString("Single use, and only you can confirm it.")
	return telegram.Response{Body: b.String()}, nil
}

// confirmRelease is phase two: redeem the token and promote, atomically.
func (d ReleaseDeps) confirmRelease(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	token := releases.NormalizeToken(req.Args[0])
	if !releases.ValidToken(token) {
		return telegram.Response{Body: "That is not a confirmation token. Send " +
			telegram.Code("/release") + " to start."}, nil
	}

	result, err := d.Trains.PromoteConfirmed(ctx, d.Confirmations, token, req.UserID)
	if err != nil {
		return telegram.Response{Body: releaseErrorBody(err)}, nil
	}

	var b strings.Builder
	b.WriteString(telegram.Bold("Train #"+strconv.FormatInt(result.Train.ID, 10)+" promoted") + "\n\n")
	b.WriteString(trainSummary(result.Train) + "\n")
	if rng := shaRange(result.Train); rng != "" {
		b.WriteString("Range: " + telegram.Code(rng) + "\n")
	}
	if result.Split && result.FollowOn != nil {
		b.WriteString("\n" + telegram.Bold("Split after a rollback") + "\n")
		b.WriteString(fmt.Sprintf("Part %d of %d now: %s\n", result.Part, result.Parts,
			telegram.Esc(releases.FormatPRList(result.Train.PRNumbers))))
		b.WriteString("Part 2 (train #" + strconv.FormatInt(result.FollowOn.ID, 10) + "): " +
			telegram.Esc(releases.FormatPRList(result.FollowOn.PRNumbers)) +
			", due " + d.stamp(result.FollowOnWindow) + "\n")
		b.WriteString("Bisecting: if part 1 regresses, the suspects are halved.")
	}
	b.WriteString("\nRecorded as DEPLOYING. The deploy pipeline takes it from here.")
	return telegram.Response{Body: b.String()}, nil
}

// releaseErrorBody maps a promotion failure onto one operator-readable line.
// Nothing here reports success on a failed write.
func releaseErrorBody(err error) string {
	switch {
	case errors.Is(err, releases.ErrTokenUnknown):
		return "Unknown confirmation token. Send " + telegram.Code("/release") + " to start again."
	case errors.Is(err, releases.ErrTokenExpired):
		return "That token expired. Send " + telegram.Code("/release") + " for a new one."
	case errors.Is(err, releases.ErrTokenConsumed):
		return "That token was already used. Nothing was promoted twice."
	case errors.Is(err, releases.ErrTokenForeign):
		return "That token belongs to whoever requested it. Send " + telegram.Code("/release") + " to request your own."
	case errors.Is(err, releases.ErrTokenMismatch):
		return "The train changed since you asked. Send " + telegram.Code("/release") + " again."
	case errors.Is(err, releases.ErrEmptyTrain):
		return "Pending train is empty. Nothing to release."
	case errors.Is(err, releases.ErrNoOpenTrain):
		return "No open release train."
	case errors.Is(err, releases.ErrTrainHeld):
		return "The train is held for its next window. It will roll forward on its own."
	case errors.Is(err, releases.ErrTrainStateConflict):
		return "The train moved while that was in flight. Send " + telegram.Code("/release") + " again."
	default:
		return releaseUnavailable
	}
}

// ---------------------------------------------------------------------------
// /hold
// ---------------------------------------------------------------------------

// hold skips the next window only. The reply always names both the window that
// was skipped and the one the train now goes in: "held" with no dates is the
// kind of answer that turns into a forgotten train.
func (d ReleaseDeps) hold(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	if d.Trains == nil {
		return telegram.Response{Body: releaseUnavailable}, nil
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()

	reason := strings.TrimSpace(strings.Join(req.Args, " "))
	result, err := d.Trains.Hold(scoped, req.UserID, reason)
	switch {
	case errors.Is(err, releases.ErrNoOpenTrain):
		return telegram.Response{Body: "No open release train to hold."}, nil
	case errors.Is(err, releases.ErrAlreadyHeld):
		if status, serr := d.Trains.Status(scoped); serr == nil && status.Held {
			return telegram.Response{Body: "Already held.\n\n" + d.heldBody(status)}, nil
		}
		return telegram.Response{Body: "The train is already held for its next window."}, nil
	case err != nil:
		return telegram.Response{Body: releaseUnavailable}, nil
	}

	var b strings.Builder
	b.WriteString(telegram.Bold("Train #"+strconv.FormatInt(result.Train.ID, 10)+" held") + "\n\n")
	b.WriteString(trainSummary(result.Train) + "\n")
	b.WriteString("Skipped window: " + d.stamp(result.SkippedWindow) + "\n")
	b.WriteString("Next window:    " + d.stamp(result.NextWindow) + "\n")
	if result.Train.HeldReason != "" {
		b.WriteString("Reason: " + telegram.Esc(result.Train.HeldReason) + "\n")
	}
	b.WriteString("\nThe hold covers that one window and expires by itself. " +
		"The train rolls forward; nothing is dropped.")
	return telegram.Response{Body: b.String()}, nil
}

// heldBody describes an active hold.
func (d ReleaseDeps) heldBody(status releases.Status) string {
	var b strings.Builder
	b.WriteString(telegram.Bold("Train #"+strconv.FormatInt(status.Train.ID, 10)+" is held") + "\n\n")
	b.WriteString(trainSummary(status.Train) + "\n")
	b.WriteString("Skipped window: " + d.stamp(status.SkippedWindow) + "\n")
	b.WriteString("Next window:    " + d.stamp(status.NextWindow) + "\n")
	if status.Train.HeldReason != "" {
		b.WriteString("Reason: " + telegram.Esc(status.Train.HeldReason))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Rendering helpers. Named with a release prefix or a distinct noun so they
// cannot collide with the read commands' helpers in this package.
// ---------------------------------------------------------------------------

// trainSummary is the PR line every reply shares. It lists every PR rather
// than a count: "8 changes" is not something an operator can act on.
func trainSummary(train releases.Train) string {
	count := len(train.PRNumbers)
	noun := "PRs"
	if count == 1 {
		noun = "PR"
	}
	return fmt.Sprintf("%d/%d %s: %s", count, releases.MaxTrainPRs, noun,
		telegram.Esc(releases.FormatPRList(train.PRNumbers)))
}

// shaRange renders base..head when both are known.
func shaRange(train releases.Train) string {
	if train.BaseSHA == "" || train.HeadSHA == "" {
		return ""
	}
	return shortSHA(train.BaseSHA) + ".." + shortSHA(train.HeadSHA)
}

// stamp renders a window in the operator's zone. An unset window says so
// rather than rendering the zero time as a date in year 1.
func (d ReleaseDeps) stamp(at time.Time) string {
	if at.IsZero() {
		return "not scheduled"
	}
	return telegram.Esc(at.In(d.location()).Format("Mon 02 Jan 15:04 MST"))
}

// shortDuration renders a token TTL as "5 minutes".
func shortDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	switch {
	case minutes >= 2:
		return strconv.Itoa(minutes) + " minutes"
	case minutes == 1:
		return "1 minute"
	default:
		return strconv.Itoa(int(d.Seconds())) + " seconds"
	}
}
