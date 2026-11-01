import { decodeBuilderSelection } from "../../multi-match-builder/builder-state";
import { decodeSlipToken } from "../../lib/slipToken";
import { marketLabel, marketSelectionLabel } from "../../lib/markets";
import { brandShareCard, slipShareCard } from "../../lib/shareCard";

/**
 * The share card for a slip.
 *
 * A route handler rather than an `opengraph-image` file, and that is forced:
 * the file convention receives only `params`, and a builder slip lives entirely
 * in `?ticket=`. The page's `generateMetadata` does see searchParams, so it
 * points `og:image` here with the same token the reader shared.
 *
 * Everything needed is inside the token -- market and selection per leg -- so
 * the card renders without a single upstream call. That matters because a
 * popular slip is fetched once per chat app that unfurls it, and those arrive
 * together.
 */
export const runtime = "nodejs";

export async function GET(request: Request) {
  const url = new URL(request.url);

  // An ACCA carries its own summary, because it is shared with a fragment and
  // the server never sees which slip was meant.
  const slip = decodeSlipToken(url.searchParams.get("slip") ?? "");
  if (slip) {
    return slipShareCard({
      heading: slip.label || `${slip.legs.length}-leg ACCA`,
      legs: slip.legs.map((leg) => `${leg.market}: ${leg.selection}`),
      odds: slip.odds,
      footnote: "Published SokaLens predictions · not a bookmaker",
    });
  }

  const ticket = url.searchParams.get("ticket") ?? "";
  const legs = decodeBuilderSelection(ticket);

  // A malformed or expired token still deserves a branded card rather than a
  // broken preview; `decodeBuilderSelection` returns [] for anything it does
  // not trust, which is also the shape of an empty slip.
  if (legs.length === 0) {
    return brandShareCard("Build one ticket across every published market.");
  }

  return slipShareCard({
    heading: `${legs.length}-leg Builder slip`,
    legs: legs.map((leg) => `${marketLabel(leg.marketKey)}: ${marketSelectionLabel(leg.marketKey, leg.selection)}`),
    footnote: "Built on SokaLens · every leg a published prediction",
  });
}
