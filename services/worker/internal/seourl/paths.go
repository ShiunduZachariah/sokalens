package seourl

import (
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Canonical paths, mirroring apps/web/app/lib/seo.ts.
//
// This duplication is across a language boundary and is the most dangerous
// kind in this codebase, so it is worth being explicit about why it exists and
// what keeps it honest.
//
// The first version of seo_urls built paths from the database slug columns and
// guessed the prefixes. Every one of the 21,377 rows was wrong:
//
//	stored                                        actual
//	/football/teams/team-aalen                    /teams/team-aalen            404
//	/predictions/league-world-uefa-...            /predictions/leagues/...     404
//	/predictions/league-italy-serie-c-girone-a/   /predictions/serie-c-girone-a/
//	  team-italy-trento-vs-...                      trento-vs-...              308
//
// Nothing broke, because nothing read the table yet. Wiring the sitemap to it
// would have advertised twelve thousand dead URLs to a crawler that visits
// roughly 275 a day -- far worse than the under-advertising it was meant to
// fix.
//
// Teams and leagues turn out to use the stored slug and only needed the right
// prefix. Fixtures do not: the route is built from the *display names* run
// through seoSlug, so "Serie C Girone A" becomes serie-c-girone-a while the
// slug column holds league-italy-serie-c-girone-a. That is why the worker
// cannot avoid reproducing seoSlug, and why TestGoAndTypeScriptSlugsAgree
// pins the two implementations against the same inputs.

// seoSlug mirrors seoSlug in apps/web/app/lib/seo.ts.
//
//	value.normalize("NFKD")
//	     .replace(/[̀-ͯ]/g, "")
//	     .toLowerCase()
//	     .replace(/[^a-z0-9]+/g, "-")
//	     .replace(/^-+|-+$/g, "") || "football"
//
// The "football" fallback matters: a name that slugifies to nothing must not
// produce an empty path segment, because "/predictions//x-vs-y-1" is a URL
// that resolves nowhere and would be advertised as if it did.
func seoSlug(value string) string {
	decomposed, _, err := transform.String(
		transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC),
		value,
	)
	if err != nil {
		decomposed = value
	}
	lowered := strings.ToLower(decomposed)

	var b strings.Builder
	b.Grow(len(lowered))
	lastDash := false
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		// Every run of non-alphanumerics collapses to one dash, matching the
		// `+` in the JavaScript character class.
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "football"
	}
	return slug
}

// FixturePath mirrors seoFixturePath: /predictions/{league}/{home}-vs-{away}-{id}.
//
// Built from names rather than slug columns because that is what the route
// does. Using the slug column produced a 308 to the real URL -- not a 404, so
// it would have looked fine in a status check while spending a redirect of
// crawl budget on every fixture in the sitemap.
func FixturePath(leagueName, homeTeam, awayTeam string, matchID int64) string {
	if matchID <= 0 {
		return ""
	}
	if strings.TrimSpace(leagueName) == "" || strings.TrimSpace(homeTeam) == "" || strings.TrimSpace(awayTeam) == "" {
		// A missing name yields a path that resolves nowhere. Returning empty
		// lets ValidPath reject it rather than composing a plausible-looking
		// URL out of a "football" fallback that points at no fixture.
		return ""
	}
	return "/predictions/" + seoSlug(leagueName) +
		"/" + seoSlug(homeTeam) + "-vs-" + seoSlug(awayTeam) + "-" + strconv.FormatInt(matchID, 10)
}

// ValidCatalogSlug mirrors validCatalogSlug in apps/web/app/lib/catalogSlug.ts:
//
//	/^[a-z0-9]+(?:-[a-z0-9]+)*$/  and not "null", "undefined" or "nan"
//
// The route guards its slug with this and answers 404 for anything else. The
// catalogue does not: 186 team slugs in production carry the original
// diacritics -- team-denmark-noerresundby is stored as team-denmark-nørresundby,
// and likewise lillestrøm, piłkarska. Every one of those is a page that does
// not exist.
//
// Advertising a 404 in a sitemap is worse than omitting the URL. Omitting it
// costs one page of reach; advertising it teaches the crawler the file is
// unreliable, and this property has one shot at rebuilding that trust after a
// 99% impressions collapse.
func ValidCatalogSlug(slug string) bool {
	if slug == "" || slug == "null" || slug == "undefined" || slug == "nan" {
		return false
	}
	if strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return false
	}
	prevDash := false
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			prevDash = false
		case r == '-':
			// The pattern is groups joined by single dashes, so a doubled
			// dash is not a valid slug however harmless it looks.
			if prevDash {
				return false
			}
			prevDash = true
		default:
			return false
		}
	}
	return true
}

// TeamPath mirrors the /teams/[slug] route.
//
// The stored slug is already the route's form, so only the prefix was wrong --
// but it must still pass the route's own slug guard, or the sitemap advertises
// pages that 404.
func TeamPath(slug string) string {
	slug = strings.TrimSpace(slug)
	if !ValidCatalogSlug(slug) {
		return ""
	}
	return "/teams/" + slug
}

// LeaguePath mirrors seoLeaguePath: /predictions/leagues/{slug}.
func LeaguePath(slug string) string {
	slug = strings.TrimSpace(slug)
	if !ValidCatalogSlug(slug) {
		return ""
	}
	return "/predictions/leagues/" + slug
}
