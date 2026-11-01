import { notFound, permanentRedirect } from "next/navigation";
import { fetchMatchById } from "../../data/liveMatches";
import { seoFixturePath } from "../../lib/seo";

type LiveMatchPageProps = {
  params: Promise<{ id: string }>;
};

function matchIdFrom(value: string) {
  const id = Number(value);
  return Number.isInteger(id) && id > 0 ? id : null;
}

/**
 * The retired shadow fixture route.
 *
 * `/matches/{id}` and `/predictions/{league}/{home}-vs-{away}-{id}` rendered
 * the same fixture from the same data. The first was `noindex, follow`
 * whenever no prediction had been published, which is how 5,306 URLs -- 64% of
 * every non-indexed page on the property -- ended up on a route that was still
 * internally linked and still crawled every cycle.
 *
 * Nothing renders here any more. The route resolves the fixture by id and
 * hands the request to the single canonical URL, so an old link, an old
 * bookmark and an old search result all land on the page that can rank. The
 * detail view moved to `app/components/MatchDetail.tsx`.
 *
 * It redirects unconditionally, including for fixtures with no published
 * prediction: the canonical route renders those too (noindexed until a
 * prediction lands). Redirecting them to a hub instead would turn 5,306 live
 * URLs into 5,306 soft 404s, which is a worse outcome than the one being
 * fixed.
 */
export default async function LiveMatchRedirect({ params }: LiveMatchPageProps) {
  const { id: rawId } = await params;
  const id = matchIdFrom(rawId);
  if (!id) notFound();

  const match = await fetchMatchById(id);
  if (!match) notFound();

  permanentRedirect(seoFixturePath(match));
}
