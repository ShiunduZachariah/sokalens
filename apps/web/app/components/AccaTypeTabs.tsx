import Link from "next/link";
import { accaMarketDefinitions, accaMarketDisplayLabel } from "../acca-predictions/accaMarkets";
import type { Locale } from "../i18n/config";
import { localePath } from "../i18n/config";

/**
 * The six ACCA views, labelled as the design labels them.
 *
 * Two things the design is specific about and this did not do. The market tabs
 * read "Match Result ACCAs", not "Match Result" -- on a page that also lists
 * individual predictions, "Match Result" alone does not say which of the two
 * it leads to. And the first tab on the catalogue is "Individual predictions",
 * a way OUT to the single-prediction list, because "All ACCAs" on the page
 * that already is all ACCAs is a link to itself.
 *
 * On a type page that first slot stays "All ACCAs", which is the way back.
 * The design's artboard is the catalogue, so it only ever shows the first of
 * those two states; dropping "All ACCAs" everywhere would strand the five type
 * pages, which is a thing this file was already changed once to fix.
 *
 * Shared between `/acca-predictions` and the five type pages because they are one
 * artboard with a `preset` prop in the design, and because these tabs are the
 * only links between the six. A type page reached from a search result
 * previously had one route onward -- "All ACCA markets" -- so the other four
 * types were unreachable from it.
 *
 * `current` names the slug of the page drawing the tabs, or `null` on the
 * catalogue itself. It drives `aria-current`, which is what tells a screen
 * reader which of six near-identical links is the page already open.
 */
export function AccaTypeTabs({ current, locale }: { current: string | null; locale: Locale }) {
  return (
    <nav aria-label="ACCA types" className="ac-tabs">
      {current === null ? (
        <Link className="td-subnav__pill" href={localePath("/predictions/today", locale)} prefetch={false}>
          Individual predictions
        </Link>
      ) : (
        <Link className="td-subnav__pill" href={localePath("/acca-predictions", locale)} prefetch={false}>
          All ACCAs
        </Link>
      )}
      {accaMarketDefinitions.map((definition) => (
        <Link
          aria-current={current === definition.slug ? "page" : undefined}
          className="td-subnav__pill"
          href={localePath(`/acca-predictions/${definition.slug}`, locale)}
          key={definition.slug}
          prefetch={false}
        >
          {`${accaMarketDisplayLabel(definition, locale)} ACCAs`}
        </Link>
      ))}
    </nav>
  );
}
