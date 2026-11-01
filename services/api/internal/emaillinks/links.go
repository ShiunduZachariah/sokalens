// Package emaillinks is the single place an outbound email URL is built.
//
// Email is the one surface that puts SokaLens URLs in front of a reader
// outside the site, and every one of those links is a real crawl signal the
// moment a recipient forwards it, pastes it into a public channel, or a mail
// client prefetches it. The search-recovery work retired two whole URL trees
// -- `/matches/{id}` and `/leagues/{slug}` now answer with permanent
// redirects -- and `apps/web/app/lib/fixtureHref.ts` owns the one remaining
// shape for the web app, with the `seo-invariants` CI job failing the build
// over any component that writes a fixture href by hand.
//
// That gate reads `apps/web/app` only. Notification email bodies are built in
// Go, so a kickoff reminder linking `/matches/1234567` would be invisible to
// it and would ship a redirect hop into every inbox. This package is the Go
// half of the same rule: it can only produce canonical URLs, and
// `apps/web/scripts/seo-invariants.mjs` fails the build if any Go email
// surface spells a URL out for itself instead of calling it.
//
// The slug algorithm is a deliberate mirror of `seoSlug` in
// `apps/web/app/lib/seo.ts`. `packages/contracts/seo/email-link-cases.json`
// holds the shared case table and is asserted from both languages, so the two
// implementations cannot drift into producing different URLs for the same
// fixture.
package emaillinks

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// TodayHub is the destination for a fixture whose identifying data is
// incomplete. Linking a reader to a real hub is better than linking them to a
// URL that resolves to nothing; it matches the web app's own fallback.
const TodayHub = "/predictions/today"

// Builder turns canonical paths into absolute URLs against the public origin.
type Builder struct {
	// BaseURL is the public web origin, e.g. https://sokalens.com. It is
	// trimmed of a trailing slash on use.
	BaseURL string
}

// Slug mirrors seoSlug in apps/web/app/lib/seo.ts: NFKD-normalise, drop
// combining marks, lowercase, collapse every remaining non-alphanumeric run to
// a single hyphen, trim hyphens, and fall back to "football" when nothing
// survives.
//
// The fallback matters: a league name written entirely in a non-Latin script
// normalises to an empty string, and an empty path segment would produce
// `/predictions//x-vs-y-1` -- a URL that 404s and that a mail client will
// happily prefetch.
func Slug(value string) string {
	decomposed := norm.NFKD.String(value)
	var builder strings.Builder
	builder.Grow(len(decomposed))
	lastHyphen := false
	for _, r := range decomposed {
		if unicode.In(r, unicode.Mn) {
			continue
		}
		lower := unicode.ToLower(r)
		if (lower >= 'a' && lower <= 'z') || (lower >= '0' && lower <= '9') {
			builder.WriteRune(lower)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			builder.WriteByte('-')
			lastHyphen = true
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "football"
	}
	return slug
}

// FixturePath returns the one canonical fixture URL, or the today hub when the
// caller does not have enough to build it. It never returns `/matches/{id}`:
// that route answers with a permanent redirect, so an emailed link to it costs
// the reader a hop and returns no index value.
func FixturePath(fixtureID int64, leagueName, homeTeam, awayTeam string) string {
	if fixtureID <= 0 || blank(leagueName) || blank(homeTeam) || blank(awayTeam) {
		return TodayHub
	}
	return "/predictions/" + Slug(leagueName) + "/" + Slug(homeTeam) + "-vs-" + Slug(awayTeam) + "-" + strconv.FormatInt(fixtureID, 10)
}

// LeaguePath returns the canonical league hub. `/leagues/{slug}` is the
// retired twin and is never emitted.
func LeaguePath(leagueName string) string {
	if blank(leagueName) {
		return TodayHub
	}
	return "/predictions/leagues/" + Slug(leagueName)
}

// MarketHubPath returns a canonical market hub for a slug from the immutable
// market catalogue in apps/web/app/lib/seo.ts. Callers pass the catalogue slug
// itself, not a display name, because the hub set is explicit and finite: a
// hub URL invented from a market label is a 404 in an inbox.
func MarketHubPath(catalogueSlug string) string {
	slug := Slug(catalogueSlug)
	if blank(catalogueSlug) {
		return TodayHub
	}
	return "/predictions/markets/" + slug
}

// AccountPath returns a path on the authenticated, noindex side of the site --
// verification, password reset, notification preferences. These are excluded
// from every sitemap and serve `robots: index:false`, so they carry no search
// exposure; they are routed through here so that one place lists every URL
// email is allowed to produce.
//
// The token is query-escaped. An unknown page returns the today hub rather
// than a guessed path.
func AccountPath(page, token string) string {
	paths := map[string]string{
		"verify_email":             "/verify-email",
		"password_reset":           "/reset-password",
		"notification_preferences": "/notifications",
		"account":                  "/account",
	}
	path, ok := paths[page]
	if !ok {
		return TodayHub
	}
	if token == "" {
		return path
	}
	return path + "?token=" + url.QueryEscape(token)
}

// Absolute joins a canonical path onto the public origin.
//
// Nothing here appends campaign parameters. A tracked link is a second URL for
// the same page, and mail clients and link scanners fetch what they are given:
// the property has already spent a recovery cycle on 5,306 duplicate fixture
// URLs, and re-introducing a parameterised twin of every canonical page
// through email would be the same defect with a different cause. Attribution
// belongs in a first-party analytics event on arrival, not in the href.
func (b Builder) Absolute(path string) string {
	if path == "" {
		path = TodayHub
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return strings.TrimRight(b.BaseURL, "/") + path
}

// Fixture, League, MarketHub and Account are the absolute-URL forms used by
// email templates.
func (b Builder) Fixture(fixtureID int64, leagueName, homeTeam, awayTeam string) string {
	return b.Absolute(FixturePath(fixtureID, leagueName, homeTeam, awayTeam))
}

func (b Builder) League(leagueName string) string { return b.Absolute(LeaguePath(leagueName)) }

func (b Builder) MarketHub(catalogueSlug string) string {
	return b.Absolute(MarketHubPath(catalogueSlug))
}

func (b Builder) Account(page, token string) string {
	return b.Absolute(AccountPath(page, token))
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }
