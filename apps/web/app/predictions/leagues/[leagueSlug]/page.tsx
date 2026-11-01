import type { Metadata } from "next";
import { notFound } from "next/navigation";
import LeaguePage from "../../../leagues/[slug]/page";
import { fetchLeague } from "../../../data/catalog";
import { canonicalPath } from "../../../site";
import { seoLeaguePath } from "../../../lib/seo";

type LeaguePredictionPageProps = { params: Promise<{ leagueSlug: string }> };

export async function generateMetadata({ params }: LeaguePredictionPageProps): Promise<Metadata> {
  const { leagueSlug } = await params;
  const result = await fetchLeague(leagueSlug);
  if (!result.ok) {
    if (result.error === "not_found") notFound();
    return {
      title: "League data temporarily unavailable - SokaLens",
      description: "League prediction data is temporarily unavailable.",
      alternates: { canonical: canonicalPath(seoLeaguePath(leagueSlug)) },
      robots: { index: false, follow: true },
    };
  }
  const league = result.data;
  const hasContent = Boolean(league && (league.upcoming_matches.length > 0 || league.recent_matches.length > 0));
  return {
    title: `${league.name} predictions, fixtures and results - SokaLens`,
    description: `${league.name} football predictions, upcoming fixtures, recent results, and participating teams.`,
    alternates: { canonical: canonicalPath(seoLeaguePath(leagueSlug)) },
    robots: { index: hasContent, follow: true },
  };
}

export default async function LeaguePredictionPage({ params }: LeaguePredictionPageProps) {
  const { leagueSlug } = await params;
  const result = await fetchLeague(leagueSlug);
  if (!result.ok && result.error === "not_found") notFound();
  return <LeaguePage params={Promise.resolve({ slug: leagueSlug })} />;
}
