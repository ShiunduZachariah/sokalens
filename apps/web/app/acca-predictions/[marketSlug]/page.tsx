import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { AccaTypeTabs } from "../../components/AccaTypeTabs";
import { ContentPageShell } from "../../components/ContentPageShell";
import { LinkCard } from "../../components/LinkCard";
import { ResponsibleNote } from "../../components/ResponsibleNote";
import { fetchAccaPage } from "../../data/accaApi";
import { absoluteUrl, canonicalPath } from "../../site";
import { AccaExplorer } from "../AccaExplorer";
import { accaMarketDefinition, accaMarketDefinitions, accaMarketDisplayLabel } from "../accaMarkets";
import { localePath } from "../../i18n/config";
import { requestLocaleContext } from "../../i18n/server";

type Props = { params: Promise<{ marketSlug: string }> };

export const dynamic = "force-dynamic";

export function generateStaticParams() {
  return accaMarketDefinitions.map((item) => ({ marketSlug: item.slug }));
}

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const definition = accaMarketDefinition((await params).marketSlug);
  if (!definition) return { title: "ACCA market not found - SokaLens", robots: { index: false, follow: true } };
  const path = `/acca-predictions/${definition.slug}`;
  return { title: `${definition.title} - SokaLens`, description: definition.description, alternates: { canonical: canonicalPath(path) }, robots: { index: true, follow: true }, openGraph: { title: definition.title, description: definition.description, url: absoluteUrl(path) } };
}

export default async function AccaMarketPage({ params }: Props) {
  const { locale } = await requestLocaleContext();
  const definition = accaMarketDefinition((await params).marketSlug);
  if (!definition) notFound();
  let initialPage;
  try { initialPage = await fetchAccaPage(new URLSearchParams({ tab: definition.marketKeys[0] ?? "all", selection: definition.selection ?? "", period: "seven_days", limit: "10", generate: "1" })); } catch { initialPage = { bundles: [], total: 0, page: 1, pageSize: 10, facets: { markets: [], countries: [], leagues: [], legCounts: [], leagueCountries: {} } }; }
  const localizedHeading = `${accaMarketDisplayLabel(definition, locale)} ACCAs`;
  const path = `/acca-predictions/${definition.slug}`;
  const itemList = { "@context": "https://schema.org", "@type": "ItemList", name: definition.heading, url: absoluteUrl(path), itemListElement: initialPage.bundles.slice(0, 50).map((bundle, index) => ({ "@type": "ListItem", position: index + 1, name: bundle.label, url: absoluteUrl(`/acca-predictions#acca-${encodeURIComponent(bundle.id)}`) })) };

  return (
    <ContentPageShell
      accent="ACCAs"
      beforeBody={<AccaTypeTabs current={definition.slug} locale={locale} />}
      crumbs={[
        { href: "/acca-predictions", label: "ACCAs" },
        { label: accaMarketDisplayLabel(definition, locale) },
      ]}
      eyebrow="System ACCAs · rolling seven days"
      locale={locale}
      rail={
        <div className="td-panel td-panel--links">
          <h2>Keep exploring</h2>
          <LinkCard href="/acca-predictions" label="All ACCA markets" locale={locale} />
          <LinkCard href="/multi-match-builder" label="Open the Builder" locale={locale} />
          <LinkCard href="/predictions/today" label="Today&rsquo;s predictions" locale={locale} />
          <LinkCard href="/results" label="How we performed" locale={locale} />
        </div>
      }
      stats={[{ label: "Bundles", value: initialPage.total, accent: true }]}
      summary={`${definition.description} These are system-generated bundles, not user-created Builder tickets.`}
      title={accaMarketDisplayLabel(definition, locale)}
    >
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(itemList).replace(/</g, "\\u003c") }} />
      {initialPage.total > 0 ? (
        <AccaExplorer
          initialMarket={definition.marketKeys[0] ?? "all"}
          initialPage={initialPage}
          initialSelection={definition.selection}
        />
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No eligible {localizedHeading} right now</h3>
          <p>
            This market page updates automatically when enough independent fixtures clear the
            quality gates. Nothing is substituted for a thin catalogue.
          </p>
          <Link className="mp-btn" href={localePath("/acca-predictions", locale)}>
            All ACCA markets
          </Link>
        </div>
      )}
      <div className="mp-tail">
        <ResponsibleNote />
      </div>
    </ContentPageShell>
  );
}
