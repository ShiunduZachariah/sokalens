import type { Metadata } from "next";
import type { ReactNode } from "react";
import { Archivo, IBM_Plex_Mono } from "next/font/google";
import { AccountProvider } from "./components/account/AccountContext";
import { FavoritesProvider } from "./components/account/FavoritesContext";
import { AdSlot } from "./components/AdSlot";
import { AdhesionSlot } from "./components/AdhesionSlot";
import { MobileBottomNav } from "./components/MobileBottomNav";
import { WebAnalytics } from "./components/WebAnalytics";
import { localTimeScript } from "./components/LocalTime";
import { PublicHeader } from "./components/PublicHeader";
import { LanguageChooser } from "./components/locale/LanguageChooser";
import { LocaleProvider } from "./components/locale/LocaleProvider";
import { localeAlternates, localeDetails, localePath, supportedLocales } from "./i18n/config";
import { translate } from "./i18n/catalogs";
import { requestLocaleContext } from "./i18n/server";
import { siteConfig } from "./site";
import { buildOrganization, buildWebSiteSearch, structuredDataScript } from "./lib/structuredData";
import "./globals.css";
import { ADS_ENABLED } from "./lib/adsEnabled";

/*
 * The refresh's two families, self-hosted by next/font at build time.
 *
 * `preload: false` is deliberate. The site loaded no web font before this, and
 * only the refreshed pages consume these variables, so preloading on all ~60
 * routes would buy every other page a font fetch it never paints with -- and
 * spend its LCP budget doing it. The refreshed pages still get the file on
 * first paint of the text that uses it, with `display: swap` covering the gap.
 *
 * Archivo is requested WITH the `wdth` axis: the design sets font-stretch
 * between 62% and 75% on every display heading, which only the variable-width
 * font can render. Dropping the axis silently falls back to normal width and
 * loses the condensed look that carries the whole design.
 */
const archivo = Archivo({
  axes: ["wdth"],
  display: "swap",
  preload: false,
  subsets: ["latin"],
  variable: "--font-archivo",
});

const plexMono = IBM_Plex_Mono({
  display: "swap",
  preload: false,
  subsets: ["latin"],
  variable: "--font-plex-mono",
  weight: ["400", "500", "600"],
});

export async function generateMetadata(): Promise<Metadata> {
  const { locale, publicPath } = await requestLocaleContext();
  const title = translate(locale, "site.title");
  const description = translate(locale, "site.description");
  return {
    metadataBase: new URL(siteConfig.url),
    title,
    description,
    alternates: { canonical: publicPath, languages: localeAlternates(publicPath) },
    openGraph: {
      title, description, siteName: siteConfig.name, type: "website", url: publicPath,
      locale: localeDetails[locale].ogLocale,
      alternateLocale: supportedLocales.filter((item) => item !== locale).map((item) => localeDetails[item].ogLocale),
      // No `images` here on purpose. The `opengraph-image` file convention
      // supplies og:image and twitter:image for every route, and a page that
      // has its own card -- a fixture, a team -- overrides this one simply by
      // having the file. Naming an image here would beat that convention and
      // put the site's own card on every match link, which is what it did:
      // public/og.png, 1.5MB, reading "SokaStats", a brand this site does not
      // use.
    },
    twitter: { card: "summary_large_image", title, description },
    robots: { follow: true, index: true },
    // AdSense site verification. Harmless alongside the loader script and
    // gives Google a second, script-independent way to confirm ownership.
    // Omitted while ads are switched off.
    ...(ADS_ENABLED
      ? {
          other: {
            "google-adsense-account": siteConfig.adsenseClientId,
            monetag: siteConfig.monetagVerification,
          },
        }
      : {}),
  };
}

export default async function RootLayout({
  children,
}: Readonly<{
  children: ReactNode;
}>) {
  const { locale } = await requestLocaleContext();
  // Organization and WebSite are site-wide claims about the publisher, so they
  // belong in the one place every page renders. The SearchAction is a true
  // claim rather than an aspiration: /search exists and takes ?q=.
  const siteSchema = structuredDataScript([buildOrganization(), buildWebSiteSearch(locale)]);
  return (
    // The inline script below stamps data-theme on <html> before React
    // hydrates, so the client always has an attribute the server did not
    // render. suppressHydrationWarning is the sanctioned way to say that is
    // deliberate; it applies to this element's own attributes only, one level
    // deep, so a genuine mismatch anywhere inside the app is still reported.
    //
    // Rendering the attribute on the server instead would need the theme in a
    // cookie, and would still not close the gap: the default preference is
    // "system", which resolves from prefers-color-scheme and is unknowable
    // server-side. Most visitors have nothing stored, so the server would be
    // guessing for them.
    <html
      className={`${archivo.variable} ${plexMono.variable}`}
      dir={localeDetails[locale].direction}
      lang={locale}
      suppressHydrationWarning
    >
      <head>
        <script
          dangerouslySetInnerHTML={{
            __html: "(function(){try{var s=localStorage.getItem('sokalens.theme');var t=(s==='light'||s==='dark')?s:(window.matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t);}catch(e){}})();",
          }}
        />
        <script
          dangerouslySetInnerHTML={{
            __html: "(function(){try{var k='sokalens.acquisition.first';if(localStorage.getItem(k))return;var u=new URL(location.href);var r='';try{r=document.referrer?new URL(document.referrer).hostname:'';}catch(e){};var v={referrer_host:r,utm_source:u.searchParams.get('utm_source')||'',utm_medium:u.searchParams.get('utm_medium')||'',utm_campaign:u.searchParams.get('utm_campaign')||'',utm_content:u.searchParams.get('utm_content')||'',landing_page:u.pathname};localStorage.setItem(k,JSON.stringify(v));}catch(e){}})();",
          }}
        />
      </head>
      <body>
        <AccountProvider>
          <FavoritesProvider>
          <LocaleProvider initialLocale={locale}>
          {siteSchema ? <script dangerouslySetInnerHTML={{ __html: siteSchema }} type="application/ld+json" /> : null}
          <LanguageChooser />
          <a className="skip-link" href="#content">
            Skip to content
          </a>
          <div className="app-shell">
            <PublicHeader />
            {children}
            <div className="site-footer-ad-wrap">
              <AdSlot className="site-footer-ad" format="footer" placement="global-footer" />
            </div>
            <footer className="site-footer">
            <div className="site-footer__inner">
              <div className="site-footer__brand">
                <strong>SokaLens</strong>
                <p>
                  Football probabilities, explained clearly and tracked openly.
                </p>
              </div>
              <div className="site-footer__links">
                <div>
                  <strong>Explore</strong>
                  <nav aria-label="Explore">
                    <a href={localePath("/predictions/today", locale)}>Today&apos;s predictions</a>
                    <a href={localePath("/best-bets", locale)}>Best Bets</a>
                    <a href={localePath("/leagues", locale)}>Leagues</a>
                    <a href={localePath("/results", locale)}>Performance status</a>
                    <a href={localePath("/multi-match-builder", locale)}>Multi-match builder</a>
                    <a href={localePath("/login", locale)}>Sign in</a>
                    <a href={localePath("/pro", locale)}>Go Pro</a>
                  </nav>
                </div>
                <div>
                  <strong>About</strong>
                  <nav aria-label="About and policies">
                    <a href={localePath("/about", locale)}>About us</a>
                    <a href="mailto:support@sokalens.com?subject=SokaLens%20feedback">Contact and suggestions</a>
                    <a href={localePath("/acca-predictions", locale)}>Acca predictions</a>
                    <a href={localePath("/responsible-gambling", locale)}>Responsible use</a>
                    <a href={localePath("/terms", locale)}>Terms</a>
                    <a href={localePath("/privacy", locale)}>Privacy</a>
                    <a href={localePath("/cookies", locale)}>Cookies</a>
                  </nav>
                </div>
              </div>
            </div>
            {/*
              The age and responsibility treatment, on every page.

              The site publishes betting-market predictions, so this is the one
              piece of the footer that is not decoration: an age marker, a route
              to the responsible-gambling page, and the statement that SokaLens
              is not a bookmaker and holds no stakes. The last of those appeared
              only in structured data and on /about, which is to say it was
              legible to crawlers and not to readers.

              It lives in the root layout rather than per page so a new page type
              cannot ship without it -- which is the failure this is guarding
              against, not the wording.
            */}
            <div className="site-footer__bottom">
              <span className="site-footer__age" aria-label="Eighteen plus only">18+</span>
              <span>© 2026 SokaLens</span>
              <span>
                Predictions are statistical estimates, not guaranteed outcomes. SokaLens is not a
                bookmaker and accepts no stakes.{" "}
                <a href={localePath("/responsible-gambling", locale)}>Bet responsibly</a>.
              </span>
            </div>
            </footer>
            {/*
              The mobile adhesion band, fixed directly above the tab bar. In the
              layout rather than per page so a new page cannot ship without it,
              and ordered before the nav so the nav's higher z-index keeps it on
              top. AdhesionSlot excludes the Builder, whose own sticky ticket
              owns that edge.
            */}
            <AdhesionSlot />
            <MobileBottomNav />
            <WebAnalytics />
            {/*
              Runs before the first paint, after the content above has been
              parsed, so kickoff times are already in the reader's timezone
              rather than flashing UTC and correcting on hydration. Placed
              here rather than in <head> because the elements it rewrites must
              exist by the time it executes.
            */}
            <script dangerouslySetInnerHTML={{ __html: localTimeScript }} />
          </div>
          </LocaleProvider>
          </FavoritesProvider>
        </AccountProvider>
      </body>
    </html>
  );
}
