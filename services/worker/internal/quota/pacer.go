package quota

import "time"

// The pacer is the accelerator. The planner is the brake.
//
// Everything in this package before now answered "may this class of work run?"
// -- a permission. Nothing answered "how much work should we be creating?" and
// so nothing ever created any. The result is the defect this exists for: over
// two days the platform spent 12,000 requests of a 75,000/day plan. The other
// 63,000 were not saved. Quota does not roll over; unspent quota at 00:00 UTC
// is destroyed, and a request not made on Tuesday cannot be made on Wednesday.
//
// So permission was never the constraint. Having somewhere to put the work is.
//
// The shape is from the architecture doc, section 3.3:
//
//	minutes_left       = minutes until 00:00 UTC
//	committed_forecast = projected P0+P1+P2 spend for the rest of today
//	available          = quota_remaining - committed_forecast - safety_reserve
//	ballast_rate       = available / minutes_left
//
// Three properties follow from that division, and each one answers a specific
// way this could go wrong:
//
// Expiry waste. Dividing the whole surplus by the minutes remaining means the
// surplus is targeted to reach zero exactly at reset, rather than whenever the
// backlog happens to run out.
//
// Late-day dumping. The rate is recomputed every cycle against actual
// remaining quota, so it can only rise late in the day if genuine surplus
// still exists. There is no cliff and no catch-up burst -- backfill at 09:00
// is worth exactly as much as backfill at 23:00, and at 09:00 it competes with
// nothing.
//
// Early-day starvation. The committed forecast is subtracted before the
// division, and it is derived from the fixture calendar, which is known in
// advance. A Saturday with forty Tier-A matches forecasts high commitment and
// suppresses morning ballast on its own; a quiet Tuesday runs ballast hot from
// 03:00.

// lateDayHourUTC is when the reset boundary starts to matter.
//
// Reset is 00:00 UTC. European late kickoffs run to roughly 23:00 UTC, inside
// the same quota day, so the last two hours are the one time when ballast and
// live work genuinely compete. Section 3.4.
const lateDayHourUTC = 22

// lateDayBallastFactor throttles ballast inside the late window.
//
// Deliberately a throttle and not a stop. Quota unspent at midnight is
// destroyed, so refusing to spend anything after 22:00 would guarantee waste
// on exactly the nights with the most surplus. Thirty percent keeps the
// pipeline warm while leaving the bulk of what is left to the live work that
// is the reason for the boundary.
const lateDayBallastFactor = 0.30

// minutesUntilUTCMidnight is the pacer's denominator.
//
// Floored at one. At 23:59:30 the true value rounds to zero, and dividing a
// surplus by zero would either panic or, with a naive guard, report an
// unbounded rate in the single minute least able to absorb it.
func minutesUntilUTCMidnight(now time.Time) int {
	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)
	minutes := int(midnight.Sub(now).Minutes())
	if minutes < 1 {
		return 1
	}
	return minutes
}

// ballastPerMinute converts a decision's surplus into a rate.
//
// The floor it will not cross is the emergency reserve plus the daily reserve,
// both already held back rather than one or the other. That is more
// conservative than section 3.3's single absolute reserve, and the reasoning
// is that ballast is by definition the most droppable work on the platform:
// nothing breaks if a historical backfill waits a day, and a live match that
// cannot be polled is visible to every reader. Holding both reserves costs
// some ceiling and removes the case where a forecast miss leaves live work
// bidding against backfill.
//
// It still leaves the great majority of the plan spendable. On the section 3.5
// busy-Saturday model -- 15,740 committed -- this releases roughly 46,000 for
// ballast against the 12,000 a day the platform actually spent.
// ballastMinuteShare is the most of the per-minute rate limit ballast may use.
//
// The daily budget and the minute limit are different constraints and the
// division in section 3.3 only respects the first. Late in the day the
// denominator is small, so a large surviving surplus divides into a very large
// per-minute rate -- at 23:58 a 46,000 surplus computes to thousands of
// requests in a minute against a 450/min limit. Every one of those over the
// limit comes back as a rate-limit error, and this platform has already
// learned what those cost: a body-encoded 429 with no Retry-After triggered a
// sixty-second global pause and held real spend to 36 requests a minute.
//
// So ballast is capped at a minority share of the minute limit. Live work is
// bursty and unschedulable -- it happens when matches happen -- and it must
// never find the minute already spent by backfill that had all day to run.
const ballastMinuteShare = 0.40

func ballastPerMinute(decision Decision, reserve int, now time.Time) int {
	// A paused class cannot be paced. The planner has already decided the
	// surplus is not real, and pacing against it would spend the reserve the
	// pause exists to protect.
	if decision.BackfillPaused {
		return 0
	}
	available := decision.SafeDiscretionary - reserve
	if available <= 0 {
		return 0
	}
	rate := float64(available) / float64(minutesUntilUTCMidnight(now))
	if now.UTC().Hour() >= lateDayHourUTC {
		rate *= lateDayBallastFactor
	}
	paced := int(rate)
	// The minute limit is a separate constraint from the daily budget, and the
	// section 3.3 division respects only the daily one.
	if ceiling := minuteCeiling(decision.MinuteLimit); paced > ceiling {
		paced = ceiling
	}
	// Round down everywhere except the last step to zero. A surplus that
	// divides to less than one request a minute is still a surplus, and
	// truncating it to nothing would strand it for the rest of the day.
	if paced < 1 && available > 0 {
		return 1
	}
	return paced
}

// minuteCeiling is the ballast share of the per-minute rate limit.
func minuteCeiling(minuteLimit int) int {
	if minuteLimit <= 0 {
		minuteLimit = 450
	}
	ceiling := int(float64(minuteLimit) * ballastMinuteShare)
	if ceiling < 1 {
		return 1
	}
	return ceiling
}
