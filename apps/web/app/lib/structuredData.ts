import { absoluteUrl, siteConfig } from "../site";

/**
 * The shared structured-data builders.
 *
 * SportsEvent already shipped on fixture pages; this is the rest of the set.
 * Rich results move click-through most at exactly the positions the property
 * currently holds -- a breadcrumb trail or an FAQ accordion is the difference
 * between one line of blue text and four, and the site's whole problem is a
 * 0.91% CTR, not a shortage of impressions.
 *
 * Every builder returns a plain object or null. `structuredDataScript`
 * serialises whatever survives, escaping `<` so a team name containing markup
 * cannot close the script tag.
 */

export type StructuredDataNode = Record<string, unknown> | null | undefined | false;

export function structuredDataScript(nodes: StructuredDataNode[]) {
  const graph = nodes.filter(Boolean);
  return graph.length > 0 ? JSON.stringify(graph).replaceAll("<", "\\u003c") : null;
}

export type Crumb = { name: string; path: string };

/**
 * A breadcrumb trail.
 *
 * Google renders it in place of the raw URL, which is worth more on a deep
 * fixture path than anywhere else: `sokalens.com › Predictions › Premier
 * League › Arsenal vs Chelsea` reads as a place in a structure, where
 * `/predictions/premier-league/arsenal-vs-chelsea-852838` reads as a machine
 * artefact.
 */
export function buildBreadcrumbs(crumbs: Crumb[]) {
  if (crumbs.length === 0) return null;
  return {
    "@context": "https://schema.org",
    "@type": "BreadcrumbList",
    itemListElement: crumbs.map((crumb, index) => ({
      "@type": "ListItem",
      position: index + 1,
      name: crumb.name,
      item: absoluteUrl(crumb.path),
    })),
  };
}

export type FaqEntry = { question: string; answer: string };

/**
 * FAQPage.
 *
 * Only for questions the page genuinely answers in its own body -- Google
 * demotes FAQ markup whose answers are not visible on the page, and an
 * accordion of invented questions is the scaled-content pattern this property
 * can least afford to look like.
 */
export function buildFaq(entries: FaqEntry[]) {
  if (entries.length === 0) return null;
  return {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    mainEntity: entries.map((entry) => ({
      "@type": "Question",
      name: entry.question,
      acceptedAnswer: { "@type": "Answer", text: entry.answer },
    })),
  };
}

/**
 * Organization.
 *
 * The `description` is load-bearing rather than decorative: "not a bookmaker"
 * is the positioning every ad platform's gambling-adjacency review reads
 * first, and this is the machine-readable copy of it.
 */
export function buildOrganization() {
  return {
    "@context": "https://schema.org",
    "@type": "Organization",
    name: siteConfig.name,
    url: absoluteUrl("/"),
    // The generated brand card, not the retired SokaStats picture. Google may
    // surface this as the organisation logo, so it has to carry the right name.
    logo: absoluteUrl("/opengraph-image"),
    description: `${siteConfig.description} SokaLens publishes model predictions and a settled accuracy record; it is not a bookmaker and takes no bets.`,
    knowsAbout: ["Football predictions", "Match statistics", "Betting market probabilities"],
  };
}

/**
 * WebSite with a SearchAction.
 *
 * The site already has `/search`, so the sitelinks search box is a true claim
 * about a facility that exists.
 */
export function buildWebSiteSearch(inLanguage: string) {
  return {
    "@context": "https://schema.org",
    "@type": "WebSite",
    name: siteConfig.name,
    url: absoluteUrl("/"),
    inLanguage,
    potentialAction: {
      "@type": "SearchAction",
      target: { "@type": "EntryPoint", urlTemplate: `${absoluteUrl("/search")}?q={search_term_string}` },
      "query-input": "required name=search_term_string",
    },
  };
}
