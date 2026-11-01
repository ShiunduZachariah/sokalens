package commands

import (
	"context"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// budget renders AI spend. Spec §10.5.
//
// This is spend-based, not balance-based, and the underlying ledger sums
// reserved cost — an upper bound on settled spend. The reply says so, because
// a number presented as exact that is actually worst-case invites the wrong
// decision at the wrong moment.
func (d Deps) budget(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	if d.Budget == nil {
		return telegram.Response{Body: "Budget ledger " + unavailable + "."}, nil
	}

	scoped, cancel := d.scope(ctx)
	defer cancel()
	snap, err := d.Budget.Spend(scoped)
	if err != nil {
		return telegram.Response{Body: "Budget ledger " + unavailable + "."}, nil
	}

	rows := [][]string{
		{"Today", micros(snap.TodayMicros)},
		{"Month", micros(snap.MonthToDateMicros)},
		{"Projection", micros(snap.ProjectedMonthEndMicros) + " (cap " + micros(snap.MonthlyCapMicros) + ")"},
		{"Used", pct(snap.PercentOfCap)},
		{"Tier", string(snap.Tier)},
	}

	body := telegram.Bold("AI spend") + "\n\n" + telegram.PreTable(rows)
	if snap.ReservedBasis {
		body += "\nReserved basis: upper bound on settled spend."
	}
	return telegram.Response{Body: body}, nil
}
