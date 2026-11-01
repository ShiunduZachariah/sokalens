package commands

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// throughputWindow is the period /queues reports processing rate over.
const throughputWindow = 15 * time.Minute

// queues renders queue depth, throughput and dead letters. Spec §10.4.
//
// Note on priorities: ops_jobs.priority is a raw integer where larger is
// claimed first, and the schema comment warns explicitly that mapping it to
// P0-P4 needs a deliberate adapter. This command reports queues by name rather
// than inventing a P-scale the database does not have.
func (d Deps) queues(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	if d.Jobs == nil {
		return telegram.Response{Body: "Queue store " + unavailable + "."}, nil
	}

	scoped, cancel := d.scope(ctx)
	defer cancel()
	depth, err := d.Jobs.QueueDepth(scoped, 0)
	if err != nil {
		return telegram.Response{Body: "Queue store " + unavailable + "."}, nil
	}

	body := telegram.Bold("Queue depth") + "\n\n"
	if len(depth.Queues) == 0 {
		body += telegram.Pre("all queues empty")
	} else {
		rows := make([][]string, 0, len(depth.Queues))
		for _, q := range depth.Queues {
			oldest := "-"
			if q.Waiting > 0 {
				oldest = age(q.OldestWaitingAge)
			}
			rows = append(rows, []string{
				truncateCell(q.Queue, 16),
				strconv.FormatInt(q.Waiting, 10),
				oldest,
			})
		}
		body += telegram.PreTable(rows)
		if depth.Truncated {
			body += "\n(more queues not shown)"
		}
	}

	body += "\n" + d.renderThroughput(ctx)

	dlq := fmt.Sprintf("DLQ %d", depth.TotalDeadLettered)
	if depth.TotalDeadLettered > 0 {
		dlq = "⚠️ " + dlq
	}
	body += "\n" + telegram.Pre(dlq)
	return telegram.Response{Body: body}, nil
}

func (d Deps) renderThroughput(ctx context.Context) string {
	scoped, cancel := d.scope(ctx)
	defer cancel()
	rate, err := d.Jobs.ProcessingRate(scoped, throughputWindow, 0)
	if err != nil {
		return telegram.Pre("throughput " + unavailable)
	}
	return telegram.Pre(fmt.Sprintf("Processing %.1f/min · failures %s (%s window)",
		rate.PerMinute, pct(rate.FailureRate*100), age(rate.Window)))
}
