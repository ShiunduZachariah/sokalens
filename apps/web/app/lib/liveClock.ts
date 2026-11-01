/**
 * Whether a live match's stored minute may still be shown as the current one.
 *
 * The defect this exists for is a match reading "second half, 67'" hours after
 * kickoff. Nothing here ever ticked that number forward -- `status_elapsed` is
 * rendered exactly as the API returns it -- so the number was never wrong when
 * it was written. It went wrong by staying.
 *
 * The server now records `live_state_at`, the moment a provider last confirmed
 * a match's live state, and the worker's reaper stops a match claiming to be in
 * play once it is past every plausible duration. This is the presentation half:
 * between a missed poll and the reaper's hard bound there is a window where the
 * match probably *is* live but the minute is no longer something we know.
 *
 * ON THE THRESHOLD LIVING HERE. The worker has its own thresholds -- when to
 * spend a request reconciling, when to stop believing a match is in play at
 * all. Those are write-side decisions with different consequences, and this is
 * not a copy of them. This one answers a narrower question that only the render
 * path asks: may I print this number as if it were now? A shared constant would
 * couple two rules that should be free to move independently, and the value
 * here is chosen against how a scoreboard reads to a person, not against a poll
 * budget.
 */

/**
 * How long a confirmation stands before the minute stops being asserted.
 *
 * Live polling runs on a 30-second cadence at best and slower under quota
 * pressure, so this has to absorb a missed poll or two without flickering. Two
 * minutes is long enough that a healthy match never shows the indicator and
 * short enough that a stranded one is caught well before a person would notice
 * the clock had frozen.
 *
 * It also has to survive page caching to be worth anything. The match feeds
 * revalidate on a 15-second interval, so the rendered label is recomputed at
 * least that often and the window is eight times longer than the staleness the
 * cache itself can introduce. A viewer sees the indicator within one revalidate
 * of the server recording a stale confirmation, not on their next hard refresh.
 */
export const LIVE_CLOCK_TRUST_WINDOW_MS = 120_000;

/** Statuses whose displayed minute is a claim about the present moment. */
const CLOCK_BEARING_STATUSES = new Set(["live", "second_half", "extra_time"]);

export type LiveClockInput = {
  status: string;
  rawStatus: string;
  liveStateAt: string;
};

/**
 * A match whose clock we can still vouch for.
 *
 * A match with no recorded confirmation is treated as trustworthy rather than
 * stale. Every row written before the freshness column existed has an empty
 * value, and showing "awaiting update" against the entire back catalogue would
 * be a worse lie than the one being fixed. Those rows are bounded by the
 * server-side reaper instead, which works from elapsed time since kickoff and
 * needs no stamp.
 */
export function liveClockIsCurrent(match: LiveClockInput, now: number = Date.now()): boolean {
  if (!isClockBearing(match)) return true;
  if (!match.liveStateAt) return true;
  const confirmedAt = Date.parse(match.liveStateAt);
  if (Number.isNaN(confirmedAt)) return true;
  // A confirmation timestamped in the future is a clock-skew artefact, not
  // evidence of staleness.
  if (confirmedAt > now) return true;
  return now - confirmedAt < LIVE_CLOCK_TRUST_WINDOW_MS;
}

/**
 * Whether this match's status implies a running minute at all.
 *
 * Half-time and the break before extra time are live states that show a label,
 * not a number, so they have no clock to go stale. Marking them "awaiting
 * update" would report a problem where none exists: a match sitting at HT for
 * fifteen minutes is just half-time.
 */
export function isClockBearing(match: LiveClockInput): boolean {
  const raw = match.rawStatus.trim().toUpperCase();
  if (raw === "HT" || raw === "BT" || raw === "P" || raw === "PEN") return false;
  if (raw === "1H" || raw === "2H" || raw === "ET") return true;
  return CLOCK_BEARING_STATUSES.has(match.status);
}
