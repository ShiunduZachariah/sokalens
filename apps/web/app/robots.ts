import type { MetadataRoute } from "next";
import { siteConfig } from "./site";

/**
 * robots.txt.
 *
 * This file was `User-Agent: * / Allow: /` until 2026-09-21, which invited
 * every crawler on the internet to take the whole catalogue -- tens of
 * thousands of fixture, team, league and market URLs, multiplied across seven
 * locales.
 *
 * They accepted. Measured over one production log window: Amazonbot 32.9% of
 * all origin requests, Meta 30.6%, AhrefsBot 16.4%, other declared bots 11.5%.
 * Plausibly human: 3.3%. Googlebot: 0.3%, forty-six requests against
 * Amazonbot's 4,771 -- a hundred to one against the only crawler that sends
 * this site any readers.
 *
 * The rule below is therefore not "block bots". It is "stop subsidising the
 * crawlers that take the catalogue and return nothing", while leaving every
 * crawler that search, revenue or sharing depends on completely untouched.
 *
 * What must never be listed here, and why:
 *
 *   Googlebot, Bingbot   Search. The entire product depends on being found.
 *   Google-Adstxt        Reads /ads.txt. Blocking it breaks AdSense approval.
 *   Mediapartners-Google Serves AdSense. Blocking it stops ad revenue.
 *   facebookexternalhit  Link previews. A shared match card on WhatsApp or
 *                        Facebook renders through this agent, and that is how
 *                        football content actually spreads. It is a reader
 *                        feature, not a crawler.
 *   AhrefsBot            The operator's own SEO tooling. Ahrefs can only
 *                        report on sites it is allowed to crawl, so blocking
 *                        it would blind the very reports used to diagnose
 *                        problems like this one.
 *
 * Note that robots.txt is advisory. Amazonbot and Meta's agents both honour
 * it, which is why this is the first move rather than a firewall rule -- it is
 * reversible, costs nothing, and does not risk catching a real crawler in a
 * rule that matches too broadly.
 */
export default function robots(): MetadataRoute.Robots {
  return {
    rules: [
      // Everything not named below keeps full access. Listing the allowed
      // crawlers explicitly instead would mean a new search engine is blocked
      // by default, and being findable is the point of the site.
      {
        userAgent: "*",
        allow: "/",
        // A shared ACCA is /acca-predictions?slip=<token>#acca-<id>. One is
        // minted per share, so the set is unbounded, and every one of them
        // resolves to the same page -- the slip token only carries the strings
        // the share card draws. They already answer `noindex` with a canonical
        // to the clean URL, so this changes nothing about what can rank; it
        // stops an unbounded set of duplicates being *crawled* at all, on a
        // property where Googlebot was measured at 0.3% of origin requests and
        // has no allowance to spare.
        disallow: "/*?slip=",
      },
      // Google's agents, named explicitly.
      //
      // None of this grants access Googlebot did not already have: it matched
      // the wildcard above and was never on a disallow list. What the explicit
      // groups buy is that the permission is stated rather than inferred, so a
      // future edit to the wildcard cannot quietly change what search sees.
      //
      // That cuts both ways, and it is the thing to remember when editing this
      // file: robots.txt gives a crawler only the MOST SPECIFIC matching group.
      // Now that Googlebot has its own, it no longer reads the wildcard at all
      // -- so any rule that must apply to Googlebot has to be repeated here.
      // The `/*?slip=` disallow below is carried over for exactly that reason;
      // dropping it would hand the unbounded share-link variants back to
      // the one crawler whose budget this release is trying to protect.
      {
        userAgent: "Googlebot",
        allow: "/",
        disallow: "/*?slip=",
      },
      {
        userAgent: "Googlebot-Image",
        allow: "/",
        disallow: "/*?slip=",
      },
      // Serves AdSense. It fetches whatever page an ad will appear on in order
      // to target it, so it carries no parameter rule: a share link that
      // renders ads must stay fetchable or the slot fills with untargeted
      // filler. It indexes nothing, so it costs no crawl budget.
      {
        userAgent: "Mediapartners-Google",
        allow: "/",
      },
      // Reads /ads.txt only. Blocking it fails AdSense site approval outright.
      {
        userAgent: "Google-Adstxt",
        allow: "/",
      },
      // Amazon's crawler, for Alexa and its AI training corpus. It sent no
      // referrals in any window measured while taking a third of all traffic.
      {
        userAgent: "Amazonbot",
        disallow: "/",
      },
      // Meta's AI training agents. Distinct from facebookexternalhit above,
      // which stays allowed so shared links keep rendering a preview.
      //
      // Worth knowing when reading the traffic: 4,333 of the 4,395 requests
      // from Meta's address range arrived with an ordinary Chrome user agent
      // rather than either of these names, and only 62 identified honestly.
      // A crawler that disguises itself will not read this file either, so
      // these two lines are the polite half of the fix and a Cloudflare rule
      // on the address range is the half that actually bites.
      {
        userAgent: "meta-externalagent",
        disallow: "/",
      },
      {
        userAgent: "meta-externalfetcher",
        disallow: "/",
      },
      // The remaining AI training crawlers, none of which send readers.
      // ClaudeBot is on this list for the same reason as the rest: this is a
      // measurement about traffic, not a judgement about any operator.
      {
        userAgent: "GPTBot",
        disallow: "/",
      },
      {
        userAgent: "ClaudeBot",
        disallow: "/",
      },
      {
        userAgent: "CCBot",
        disallow: "/",
      },
      {
        userAgent: "Bytespider",
        disallow: "/",
      },
    ],
    sitemap: `${siteConfig.url}/sitemap.xml`,
  };
}
