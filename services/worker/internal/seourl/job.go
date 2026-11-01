package seourl

import (
	"context"
	"log/slog"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

// Job keys and queue for the crawlable-URL sweep.
const (
	// Queue is deliberately its own, not one of the provider queues.
	//
	// The sweep touches no provider. Putting it on provider-fixtures would
	// make it compete for the slots live polling needs and subject it to the
	// exclusive fixtures lock, for work that only reads our own tables.
	Queue = "seo"

	// VolatileJobKey re-derives fixtures, whose answer changes with the clock.
	VolatileJobKey = "seo.refresh-urls.volatile"

	// StableJobKey re-derives teams, leagues and countries.
	//
	// A separate key rather than a flag in the payload, so the two bands have
	// separate idempotency, separate schedules and separate failure histories.
	// A stable sweep failing at 3am should not look like live fixtures having
	// stopped being maintained.
	StableJobKey = "seo.refresh-urls.stable"
)

// Handler runs one band of the sweep.
type Handler struct {
	Sweep    Sweep
	Volatile bool
	Logger   *slog.Logger
}

// Run performs the sweep for this handler's band.
//
// A sweep failure is returned rather than swallowed. This is one of the few
// jobs where failing loudly is right: the whole point of the table is that
// nothing else re-derives indexability, so a sweep that silently stops leaves
// the crawlable set frozen at whatever it was, and frozen looks exactly like
// working until fixtures start ageing out of an index that still advertises
// them.
func (h Handler) Run(ctx context.Context, job jobs.Job) error {
	result, err := h.Sweep.Run(ctx, h.Volatile)
	if err != nil {
		return err
	}
	if result.Truncated {
		// Not an error -- the next run resumes and the retirement window is
		// three days -- but worth saying, because a sweep that is permanently
		// truncated is a catalogue that has outgrown its batch ceiling and
		// whose tail never gets re-derived.
		h.logger().Warn("seo url sweep hit its batch ceiling",
			"job_id", job.ID, "volatile", h.Volatile, "examined", result.Examined)
	}
	if result.HistoryGated > 0 {
		// The number the rollout decision is made on. In report-only mode this
		// line is the entire output of the run, so it is logged at info and
		// says plainly whether it withheld anything or only counted.
		h.logger().Info("seo url history gate",
			"job_id", job.ID,
			"examined", result.Examined,
			"gated", result.HistoryGated,
			"enforced", h.Sweep.EnforceHistoryGate)
	}
	return nil
}

func (h Handler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}
