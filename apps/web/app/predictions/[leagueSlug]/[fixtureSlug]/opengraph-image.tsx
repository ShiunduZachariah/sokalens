import { fixtureIdFromSeoSlug } from "../../../lib/seo";
import { SHARE_CARD_CONTENT_TYPE, SHARE_CARD_SIZE, brandShareCard, fixtureShareCard } from "../../../lib/shareCard";
import { fetchMatchById } from "../../../data/liveMatches";
import { formatInstant } from "../../../lib/format";
import { marketLabel, marketSelectionLabel } from "../../../lib/markets";

/**
 * The card for a shared fixture link: both crests, both names, the competition,
 * the kickoff, and the published call.
 *
 * This is the most-shared URL on the site and it used to render as the site's
 * own homepage card -- wrong brand, no teams, no match. In a group chat that
 * card is the entire advert for the link.
 *
 * Kickoff is formatted in UTC and labelled, because an image cannot be
 * localised after the fact the way a `<time>` element can: it is rasterised
 * once and cached by whoever shares it. An explicit zone is honest; an
 * unlabelled local time would be wrong for most readers.
 */
export const alt = "Match prediction on SokaLens";
export const size = SHARE_CARD_SIZE;
export const contentType = SHARE_CARD_CONTENT_TYPE;

type Props = { params: Promise<{ leagueSlug: string; fixtureSlug: string }> };

export default async function Image({ params }: Props) {
  const { fixtureSlug } = await params;
  const id = fixtureIdFromSeoSlug(fixtureSlug);
  const match = id ? await fetchMatchById(id).catch(() => null) : null;

  // A card is never the reason a page fails. If the fixture cannot be read, the
  // link still gets a branded image rather than a broken preview.
  if (!match) {
    return brandShareCard("Football predictions with the reasoning kept in view.");
  }

  const prediction = match.prediction ?? match.predictions[0] ?? null;
  const kickoff = formatInstant(match.kickoffAt, { timeZone: "UTC" });

  return fixtureShareCard({
    homeTeam: match.homeTeam,
    awayTeam: match.awayTeam,
    homeTeamLogo: match.homeTeamLogo,
    awayTeamLogo: match.awayTeamLogo,
    leagueName: match.leagueName,
    kickoffLabel: kickoff ? `${kickoff} UTC` : null,
    prediction: prediction
      ? `${marketLabel(prediction.marketKey)}: ${marketSelectionLabel(prediction.marketKey, prediction.selection)}`
      : null,
  });
}
