import Link from "next/link";
import type { Locale } from "../i18n/config";
import { localePath } from "../i18n/config";

/**
 * One row in a "keep exploring" panel: a label, and an arrow at the far edge.
 *
 * The design draws these as bordered rows rather than as a bulleted list of
 * links, and gives them a 48px minimum height. That height is the reason this
 * is a component and not a styled `<a>`: it is the tap-target floor, and it
 * has to survive a label long enough to wrap onto two lines.
 */
export function LinkCard({ href, label, locale }: { href: string; label: string; locale: Locale }) {
  return (
    <Link className="td-linkcard" href={localePath(href, locale)} prefetch={false}>
      <span>{label}</span>
      <span aria-hidden="true">&rarr;</span>
    </Link>
  );
}

/**
 * The onward-journey panel every prediction list page carries in its rail.
 *
 * The four destinations are the design's, and they are the same four on every
 * list page by intent -- a reader who has finished scanning one slate wants the
 * ranked view, the builder, the league tree or the accuracy record, whichever
 * page they happen to be standing on. Keeping the set fixed also keeps the
 * internal-link graph predictable: these four pages gain a link from every
 * prediction page on the site.
 *
 * `/results` rather than `/performance`: the design labels the destination
 * "How we performed", but `/performance` is not a route this app serves and
 * linking it would add a 404 to every prediction page.
 */
export function KeepExploringRail({ locale }: { locale: Locale }) {
  return (
    <div className="td-panel td-panel--links">
      <h2>Keep exploring</h2>
      <LinkCard href="/best-bets" label="Best Bets · top-ranked picks" locale={locale} />
      <LinkCard href="/multi-match-builder" label="Build an ACCA from these" locale={locale} />
      <LinkCard href="/leagues" label="Browse by league" locale={locale} />
      <LinkCard href="/results" label="How we performed" locale={locale} />
    </div>
  );
}
