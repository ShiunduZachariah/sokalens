import { fetchTeam } from "../../data/catalog";
import { SHARE_CARD_CONTENT_TYPE, SHARE_CARD_SIZE, brandShareCard, entityShareCard } from "../../lib/shareCard";

/**
 * The card for a shared team link: the club's crest and name, with the
 * competition it plays in.
 *
 * A team link is the second most-shared URL after a fixture, and it was showing
 * the same site-level picture as everything else.
 */
export const alt = "Team fixtures and predictions on SokaLens";
export const size = SHARE_CARD_SIZE;
export const contentType = SHARE_CARD_CONTENT_TYPE;

type Props = { params: Promise<{ slug: string }> };

export default async function Image({ params }: Props) {
  const { slug } = await params;
  const result = await fetchTeam(slug).catch(() => null);

  // Never let a share card be the reason a link looks broken.
  if (!result || !result.ok) {
    return brandShareCard("Fixtures, results and published predictions.");
  }

  const team = result.data;
  return entityShareCard({
    name: team.name,
    logo: team.logo_url,
    eyebrow: team.country || team.league_name || null,
    footnote: "Fixtures, results and published predictions",
  });
}
