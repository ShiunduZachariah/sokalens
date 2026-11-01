/**
 * Keep only fixtures a reader can still act on.
 *
 * The day pool deliberately spans the whole day, because ranking a
 * kickoff-ordered prefix excludes Europe's evening fixtures. That means it also
 * contains matches that have already started -- 36 of 258 on 2026-09-04 -- and
 * a shortlist led by a match that kicked off at 02:00 is wrong.
 *
 * Filtering is by KICKOFF TIME, not matchStatus: the live feed carried
 * matchStatus "upcoming" on a match that started at 22:30 the previous day, so
 * the status goes stale while the timestamp cannot.
 */

export type TimedFixture = {
  kickoffAt?: string | null;
  matchStatus?: string | null;
};

/** Statuses that are never actionable, whatever the clock says. */
const NOT_ACTIONABLE = new Set([
  "live",
  "in_play",
  "first_half",
  "second_half",
  "half_time",
  "extra_time",
  "penalties",
  "finished",
  "postponed",
  "cancelled",
  "abandoned",
  "suspended",
]);

export function upcomingFixtures<T extends TimedFixture>(items: readonly T[], now: Date = new Date()): T[] {
  return items.filter((item) => {
    if (NOT_ACTIONABLE.has(String(item.matchStatus ?? "").toLowerCase())) return false;

    const kickoff = Date.parse(String(item.kickoffAt ?? ""));
    // A kickoff we cannot parse is kept: dropping it would empty the page over
    // a formatting change upstream.
    if (!Number.isFinite(kickoff)) return true;
    return kickoff > now.getTime();
  });
}
