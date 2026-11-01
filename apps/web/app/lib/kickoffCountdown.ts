/**
 * How long until a fixture kicks off, as a reader would say it.
 *
 * Rendered on the server and refreshed by LiveRefresh's 15-second
 * `router.refresh()`, deliberately, rather than ticked by a client timer. The
 * match clock on this page is already server-derived for a reason -- a
 * browser-side counter keeps counting when the feed stops, and a board that
 * lies confidently is worse than one that updates every fifteen seconds. A
 * countdown to a fixed timestamp would be safe to tick client-side, but having
 * one clock on the page and one in the browser is the kind of split that
 * eventually disagrees.
 */
export function kickoffCountdown(kickoffAt: string, now: Date = new Date()): string | null {
  const kickoff = Date.parse(kickoffAt);
  if (Number.isNaN(kickoff)) return null;

  const minutes = Math.round((kickoff - now.getTime()) / 60_000);
  // Past kickoff but not yet reported live: the provider has not caught up.
  // "Kicking off" is honest about that; a negative countdown is not.
  if (minutes <= 0) return "Kicking off";
  if (minutes === 1) return "1 min";
  if (minutes < 60) return `${minutes} min`;

  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  if (rest === 0) return hours === 1 ? "1 hr" : `${hours} hrs`;
  return `${hours}h ${rest}m`;
}

/**
 * Fixtures kicking off inside the window, soonest first.
 *
 * The board exists to answer "what is about to happen", so a match three hours
 * out is noise -- today's full fixture list is where that belongs.
 */
export function startingSoon<
  T extends { kickoffAt: string; status: string; homeTeam: string; awayTeam: string },
>(
  matches: T[],
  { withinMinutes = 60, now = new Date() }: { withinMinutes?: number; now?: Date } = {},
): T[] {
  const ceiling = now.getTime() + withinMinutes * 60_000;
  const ordered = matches
    .filter((match) => {
      const kickoff = Date.parse(match.kickoffAt);
      if (Number.isNaN(kickoff)) return false;
      return kickoff > now.getTime() && kickoff <= ceiling;
    })
    .sort((a, b) => Date.parse(a.kickoffAt) - Date.parse(b.kickoffAt));

  // The same fixture can arrive twice under different ids, because the same
  // club exists twice in the catalogue: 706 names are duplicated across 776
  // surplus team rows. "Korea Republic U23 v Saudi Arabia U23" listed twice at
  // the same minute in the same competition, and both rows were real matches
  // pointing at two different away-team ids for one team.
  //
  // Two fixtures between the same named sides, kicking off at the same instant,
  // cannot both be played. Collapsing them here stops the board asserting
  // otherwise while the catalogue is deduplicated -- which is the actual fix,
  // and is a data repair rather than anything this function can do.
  const seen = new Set<string>();
  return ordered.filter((match) => {
    const key = `${match.kickoffAt}|${match.homeTeam.toLowerCase()}|${match.awayTeam.toLowerCase()}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}
