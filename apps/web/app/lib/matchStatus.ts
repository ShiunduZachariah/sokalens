/**
 * Single shared mapping for match status presentation.
 *
 * The API returns a canonical `status` enum and may also include a provider
 * `raw_status` code plus a provider `raw_status_label`. Every surface that
 * renders a match status should go through `matchStatusLabel` so the wording
 * is identical across fixtures, predictions, results, match detail, league,
 * and team pages.
 */

/** Canonical statuses from the API contract (MatchSummary.status). */
export const MATCH_STATUS_LABELS: Record<string, string> = {
  scheduled: "Scheduled",
  upcoming: "Upcoming",
  delayed: "Delayed",
  live: "Live",
  halftime: "Half-time",
  second_half: "Second half",
  extra_time: "Extra time",
  penalties: "Penalties",
  break: "Break",
  interrupted: "Interrupted",
  finished: "Full time",
  postponed: "Postponed",
  canceled: "Cancelled",
  abandoned: "Abandoned",
  suspended: "Suspended",
  awarded: "Awarded",
  walkover: "Walkover",
  // Written by the worker's reaper when a match passed every plausible
  // duration and no provider observation could resolve it. Deliberately not
  // "Full time": the result is genuinely unknown, and claiming a finish we
  // cannot substantiate would be the same class of lie as the stuck clock.
  unknown: "Result unconfirmed",
};

/**
 * Defensive mapping for provider raw status codes that occasionally leak into
 * `raw_status`. Unknown codes fall back to the provider label, then to the
 * canonical status label, then to a readable version of the raw value.
 */
export const RAW_STATUS_LABELS: Record<string, string> = {
  NS: "Upcoming",
  "1H": "First half",
  HT: "Half-time",
  "2H": "Second half",
  ET: "Extra time",
  BT: "Break",
  P: "Penalties",
  FT: "Full time",
  AET: "After extra time",
  PEN: "Penalties",
  PST: "Postponed",
  CANC: "Cancelled",
  ABD: "Abandoned",
  SUSP: "Suspended",
  INT: "Interrupted",
  AWD: "Awarded",
  WO: "Walkover",
};

/** Statuses that indicate a match is in play. */
const LIVE_STATUSES = new Set([
  "live",
  "halftime",
  "second_half",
  "extra_time",
  "penalties",
  "break",
]);

/** Statuses that indicate a match has been settled. */
const FINISHED_STATUSES = new Set([
  "finished",
  // Not a result, but no longer in play: an unresolved match must drop out of
  // the live surfaces rather than sit in them forever.
  "unknown",
  "awarded",
  "walkover",
  "postponed",
  "canceled",
  "abandoned",
  "suspended",
  "interrupted",
]);

export function isLiveStatus(status: string) {
  return LIVE_STATUSES.has(status);
}

export function isFinishedStatus(status: string) {
  return FINISHED_STATUSES.has(status);
}

/**
 * Human-readable label for a match status.
 *
 * @param status canonical status (e.g. "halftime", "finished")
 * @param rawStatus optional provider raw code (e.g. "HT", "FT")
 * @param rawStatusLabel optional provider-supplied label
 */
export function matchStatusLabel(
  status: string,
  rawStatus?: string,
  rawStatusLabel?: string,
): string {
  // The reaper deliberately overrides the last provider observation when no
  // final result arrived. That stale raw status cannot confirm a result.
  if (status === "unknown") return MATCH_STATUS_LABELS.unknown;
  const rawCode = rawStatus?.trim().toUpperCase();
  if (rawCode) {
    const rawLabel = RAW_STATUS_LABELS[rawCode];
    if (rawLabel) {
      return rawLabel;
    }
  }

  const canonicalLabel = MATCH_STATUS_LABELS[status];
  if (canonicalLabel) {
    return canonicalLabel;
  }

  if (rawStatusLabel?.trim()) {
    return rawStatusLabel.trim();
  }

  if (status.trim()) {
    return status.replace(/_/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  return "Unknown";
}
