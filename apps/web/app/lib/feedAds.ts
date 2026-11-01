/**
 * Where ads go in a scrolling feed.
 *
 * The alternative to this is a fixed slot per page, which is what the site had:
 * one `in-feed` unit between two sections, earning one impression no matter
 * whether the reader scrolled past twelve matches or three hundred. A feed that
 * grows as you scroll should earn as you scroll, so the positions are derived
 * from the row count rather than written into the page.
 *
 * It is deliberately a pure function of the count. Nothing here reads the DOM,
 * the viewport or a random seed, so the same slate produces the same positions on
 * the server and in the browser, on a reload, and after an infinite-scroll page
 * appends more rows -- the existing ads keep their places and new ones appear
 * below. A seeded or measured placement would reshuffle ads under the reader's
 * thumb on every append.
 *
 * ## The 2026-10-15 ad spec, and what changed
 *
 * The spec (section 3.1) names an exact FIRST position per page -- after the
 * first competition group on a prediction list, after the fifth ranked row on
 * Best Bets -- rather than letting the cadence choose it. `firstAfterIndex`
 * carries that. Everything after the first unit still comes from the cadence,
 * because these are infinite-scroll feeds and section 6 caps in-feed density
 * per EIGHT ROWS rather than per page.
 *
 * `FEED_AD_CADENCE` therefore moved from 10 to 8, which is denser, not sparser.
 * That is deliberate and it is a ceiling rather than a target: section 6 says
 * "at most one in-feed unit per 8 rows", and `MIN_FEED_AD_SPACING` below makes 8
 * a floor no caller can undercut. The arithmetic that makes 8 the ceiling rather
 * than an arbitrary number is section 5's 30% density cap: a prediction row is
 * 76px after the V31 row restyle and a mobile in-feed unit is a 300x250, so
 * 250 / (8 * 76 + 250) = 29%, and one row fewer would push it past 30%.
 */

/**
 * Rows between in-feed units.
 *
 * Spec section 6: at most one in-feed unit per 8 rows. This is that number used
 * as the cadence, so a long feed sits exactly at the permitted density rather
 * than below it.
 */
export const FEED_AD_CADENCE = 8;

/**
 * The closest two in-feed units may ever be, in rows.
 *
 * Separate from the cadence because `firstAfterIndex` and a caller-supplied
 * cadence can both move positions, and "at most one per 8 rows" is a promise
 * about the RESULT, not about the default. A caller asking for a tighter
 * cadence is clamped here rather than trusted.
 */
export const MIN_FEED_AD_SPACING = 8;

/**
 * Never put an ad above this many rows of content.
 *
 * A feed of seven rows with an ad after the eighth gets none, which is correct.
 * But a feed of exactly eight should not open with content and immediately end
 * on an advertisement either -- the slot would sit directly above the footer and
 * read as page furniture rather than as part of the feed. So the last position
 * is dropped unless there is at least one more row of content under it.
 */
const MIN_ROWS_BELOW = 1;

export type FeedAdOptions = {
  cadence?: number;
  /**
   * Zero-based index of the row the FIRST unit follows, per spec section 3.1.
   *
   * Omitted, the first unit falls at `cadence - 1` as it always did. Supplied,
   * it is the page's designated position: `4` on Best Bets is "after the 5th
   * ranked row", and the index of a competition group's last row is "after the
   * first competition group".
   *
   * A position with nothing below it is still dropped, so a designated position
   * at the very end of a feed yields no unit rather than page furniture --
   * which is exactly why section 3.1 qualifies the prediction-list position
   * with "only if there are 2+ groups".
   */
  firstAfterIndex?: number;
  /** Optional per-caller cap; scrolling feeds have none by default. */
  maxBlocks?: number;
};

/**
 * The zero-based row indices that an ad block follows.
 *
 * `feedAdPositions(25)` is `{7, 15, 23}`: an ad after the eighth, sixteenth and
 * twenty-fourth rows. A caller renders its rows in order and emits a block after
 * any row whose index is in the set.
 */
export function feedAdPositions(rowCount: number, options: FeedAdOptions = {}): Set<number> {
  const requested = Math.max(1, Math.floor(options.cadence ?? FEED_AD_CADENCE));
  // The density promise of spec section 6, enforced rather than assumed.
  const cadence = Math.max(MIN_FEED_AD_SPACING, requested);
  const maxBlocks = options.maxBlocks === undefined ? Infinity : Math.max(0, Math.floor(options.maxBlocks));
  const first = options.firstAfterIndex === undefined
    ? cadence - 1
    : Math.max(0, Math.floor(options.firstAfterIndex));
  const positions = new Set<number>();
  if (rowCount <= 0 || maxBlocks === 0) return positions;

  for (let index = first; index < rowCount - MIN_ROWS_BELOW; index += cadence) {
    positions.add(index);
    if (positions.size >= maxBlocks) break;
  }
  return positions;
}

/**
 * Splits a run of rows into segments, recording which ones have an ad after
 * them. The final segment can have an ad when the next league/group has more
 * content: the decision depends on the whole feed, not this run alone.
 *
 * Used so a feed can keep its rows in real `<ol>` elements and put the ad
 * *between* lists rather than inside one. An ad rendered as an `<li>` makes a
 * screen reader announce "list, 11 items" for ten predictions and an
 * advertisement, and the ad is a complementary region, not an item in the list.
 *
 * `startIndex` is the row's position in the WHOLE feed, not in this run: the
 * cadence has to carry across group and league boundaries, or a slate of
 * three-match leagues would never reach the cadence and never show an ad.
 */
export type FeedAdSegment<T> = { rows: T[]; adAfter: boolean };

export function splitForFeedAds<T>(rows: T[], startIndex: number, positions: Set<number>): FeedAdSegment<T>[] {
  const segments: FeedAdSegment<T>[] = [];
  let current: T[] = [];
  rows.forEach((row, offset) => {
    current.push(row);
    if (positions.has(startIndex + offset)) {
      segments.push({ rows: current, adAfter: true });
      current = [];
    }
  });
  // The trailing run, which may be empty when the last row of this group also
  // took an ad -- pushing it would render an empty <ol> with a border.
  if (current.length > 0) segments.push({ rows: current, adAfter: false });
  return segments;
}
