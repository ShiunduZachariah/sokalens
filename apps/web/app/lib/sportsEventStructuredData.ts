import type { LiveMatch } from "../data/liveMatches";
import { seoLeaguePath, seoSlug } from "./seo.ts";

const statusMap: Record<string, string> = {
  finished: "https://schema.org/EventCompleted",
  awarded: "https://schema.org/EventCompleted",
  walkover: "https://schema.org/EventCompleted",
  postponed: "https://schema.org/EventPostponed",
  interrupted: "https://schema.org/EventPostponed",
  suspended: "https://schema.org/EventPostponed",
  cancelled: "https://schema.org/EventCancelled",
  canceled: "https://schema.org/EventCancelled",
  abandoned: "https://schema.org/EventCancelled",
};

export function buildSportsEventStructuredData(match: LiveMatch, url: string) {
  // Google requires a physical location for Event rich results. Publishing no
  // SportsEvent is more accurate than inventing a stadium for provider rows
  // whose venue is still unknown.
  const venueName = match.venueName.trim();
  if (!venueName) return null;
  const kickoff = Date.parse(match.kickoffAt);
  if (Number.isNaN(kickoff)) return null;
  const endDate = new Date(kickoff + 2 * 60 * 60 * 1000).toISOString();
  const images = [match.homeTeamLogo, match.awayTeamLogo].filter((item) => /^https?:\/\//i.test(item));
  const country = match.country.trim();
  const venueCity = match.venueCity.trim();
  const address = country && country !== "International"
    ? { "@type": "PostalAddress", addressLocality: venueCity || undefined, addressCountry: country }
    : venueCity
      ? { "@type": "PostalAddress", addressLocality: venueCity }
      : undefined;
  if (!address) return null;
  const leagueName = match.leagueName.trim();
  const organizer = leagueName
    ? { "@type": "SportsOrganization", name: leagueName, url: new URL(seoLeaguePath(seoSlug(leagueName)), url).toString() }
    : { "@type": "SportsOrganization", name: "Football" };
  const description = `${match.homeTeam} play ${match.awayTeam}${leagueName ? ` in ${leagueName}` : ""} at ${venueName}. View the published SokaLens match predictions and settled market results.`;
  return {
    "@context": "https://schema.org",
    "@type": "SportsEvent",
    name: `${match.homeTeam} vs ${match.awayTeam}`,
    description,
    startDate: match.kickoffAt,
    endDate,
    eventStatus: statusMap[match.status.trim().toLowerCase()] ?? "https://schema.org/EventScheduled",
    location: { "@type": "Place", name: venueName, address },
    image: images.length > 0 ? images : undefined,
    organizer,
    performer: [
      { "@type": "SportsTeam", name: match.homeTeam, logo: match.homeTeamLogo || undefined },
      { "@type": "SportsTeam", name: match.awayTeam, logo: match.awayTeamLogo || undefined },
    ],
    homeTeam: { "@type": "SportsTeam", name: match.homeTeam, logo: match.homeTeamLogo || undefined },
    awayTeam: { "@type": "SportsTeam", name: match.awayTeam, logo: match.awayTeamLogo || undefined },
    url,
  };
}
