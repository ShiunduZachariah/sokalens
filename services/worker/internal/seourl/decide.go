// Package seourl decides which URLs belong in a search index, and keeps that
// decision in one place.
//
// It exists because the decision was previously made independently by the
// fixture page, the sitemap loaders and the hub pages, and those three
// disagreed three times in three weeks. The page archived on an hour-granular
// age while the sitemap advertised a whole-day window. The sitemap inherited a
// UI page cap and advertised 2,307 fixtures when 7,323 were indexable. Root and
// /en/ duplicated one another until #96, by which point Google had declined to
// index 8,318 of ~16,930 discovered URLs and impressions had fallen from 928 a
// day to 6.
//
// None of those produced an error. That is the defining hazard: a wrong
// crawlable set fails silently and reports back weeks later as traffic that
// went away. So the rules live here as pure functions over a value, the edge
// cases are tested rather than reasoned about, and everything that needs the
// answer reads the same row.
package seourl

import (
	"strings"
	"time"
)

// EntityType is the kind of thing a URL points at.
type EntityType string

const (
	TypeFixture EntityType = "fixture"
	TypeTeam    EntityType = "team"
	TypeLeague  EntityType = "league"
	TypeCountry EntityType = "country"
	TypeMarket  EntityType = "market"
	TypeDateHub EntityType = "date_hub"
	TypeStatic  EntityType = "static"
)

// Reason is why a path is withheld from the index.
type Reason string

const (
	ReasonNone         Reason = ""
	ReasonNoPrediction Reason = "no_prediction"
	ReasonArchived     Reason = "archived"
	ReasonNotPlayed    Reason = "not_played"
	ReasonThin         Reason = "thin"
	ReasonDuplicate    Reason = "duplicate"
	ReasonRetired      Reason = "retired"
	// ReasonInsufficientHistory withholds a fixture whose teams have too little
	// settled history for the page to say anything specific about them. Kept
	// distinct from ReasonThin so the excluded population can be measured on
	// its own: it is the one exclusion that resolves itself as matches are
	// played, and the count falling is how the gate proves it is working.
	ReasonInsufficientHistory Reason = "insufficient_history"
)

// MinimumFixtureHistory is how many settled matches each side needs before its
// fixture page is worth advertising.
//
// Mirrors MIN_MATCHES_FOR_RATES in apps/web/app/lib/marketReads.ts, which is
// the bar the preview builder itself applies before it will state a rate. Below
// it the page is a pick and two form strings -- the same page several thousand
// times with the nouns swapped, which is the shape that earned team pages a
// 0.14% click-through rate and left 316 of 414 in "Crawled, currently not
// indexed".
//
// This is the third constant this package shares across a language boundary,
// after SettledIndexableDays and seoSlug, and it gets the same protection: a
// test reads the TypeScript and fails if either side moves alone.
const MinimumFixtureHistory = 4

// SettledIndexableDays is how long a played fixture keeps search demand.
//
// Mirrors SETTLED_INDEXABLE_DAYS in apps/web/app/lib/fixtureLifecycle.ts. The
// duplication is across a language boundary, so only a test keeps the two
// honest -- and the comparison below is deliberately strict. An earlier
// version of the sitemap used the same number as a whole-day lookback window
// and advertised fixtures the page had already archived, because a kickoff at
// 11:00 on day -14 is 14.16 days old by the afternoon. Days are fractional
// here for exactly that reason.
const SettledIndexableDays = 14.0

// inPlayStatuses are the statuses that mean a ball is on the pitch.
//
// A match being played is never archived however old its kickoff looks, which
// matters for a fixture whose clock has stalled: the reaper may not have
// reached it yet, and quietly de-indexing a live match because a timestamp
// drifted would be a worse failure than carrying it an hour too long.
var inPlayStatuses = map[string]bool{
	"live": true, "halftime": true, "second_half": true,
	"extra_time": true, "penalties": true, "break": true,
}

// concludedStatuses are the statuses that mean the fixture is over, one way or
// another. Only these can age into the archive; anything else past kickoff is
// still considered in progress, which is the conservative reading.
var concludedStatuses = map[string]bool{
	"finished": true, "ft": true, "aet": true, "pen": true,
	"awarded": true, "walkover": true, "cancelled": true, "canceled": true,
	"postponed": true, "abandoned": true,
}

// Entity is everything the decision needs about one URL.
//
// A flat value rather than an interface: there is no behaviour to vary, and a
// struct can be constructed wrong in a test on purpose, which is most of the
// point.
type Entity struct {
	Path string
	Type EntityType
	ID   int64

	// KickoffAt is the fixture's kickoff. Zero means unknown.
	KickoffAt time.Time
	// Status is the canonical match status, lowercase.
	Status string
	// HasPrediction reports whether a current, unsuperseded prediction exists.
	HasPrediction bool

	// HomeHistory and AwayHistory count each side's settled matches before this
	// kickoff -- the same figure the API serves as home_history_count and
	// away_history_count, computed by the same predicate.
	HomeHistory int
	AwayHistory int
	// HistoryKnown reports whether the two counts above were actually supplied.
	//
	// This exists so that "no history" and "not asked" are different facts, and
	// it is not defensive padding. Without it, a source query that forgot these
	// columns would report zero for every fixture, the gate would read that as
	// "no history", and one missed wiring would silently withhold the entire
	// fixture catalogue from the index -- roughly 7,300 pages -- with no error
	// anywhere. That is the defining hazard this package was written about.
	//
	// So an unsupplied count never gates. Advertising a handful of thin pages
	// for one sweep is recoverable; de-indexing the property is the failure
	// that takes months to undo.
	HistoryKnown bool

	// HasContent reports whether a stable entity has anything worth ranking --
	// a team with a played fixture, a league with matches.
	HasContent bool

	// LastModified feeds the sitemap's lastmod. Zero means "do not claim one".
	LastModified time.Time

	// DuplicateOf marks a path superseded by a canonical twin. Non-zero means
	// this path must never be advertised whatever else is true of it.
	DuplicateOf int64
}

// Decision is the outcome for one entity.
type Decision struct {
	Indexable bool
	Reason    Reason
	// Volatile reports whether this entity's answer changes with time, and so
	// whether the sweep must keep re-deriving it.
	Volatile bool
}

// Volatile reports whether an entity type's indexability changes on its own.
//
// Fixtures are volatile: one appears, gains a prediction, is played, and
// fourteen days later stops deserving a place in the index -- four states in
// three weeks, the last driven by nothing but the clock.
//
// Teams, leagues and countries are stable. Manchester United will still be
// Manchester United next season; the row turns on whether the club has
// content, not on what time it is. The distinction is not cosmetic: it is the
// difference between re-deriving a few thousand rows a cycle and re-deriving
// 13,763 of them to discover that none changed, which is how a sweep becomes
// the thing somebody disables.
func Volatile(t EntityType) bool {
	switch t {
	case TypeFixture, TypeDateHub:
		return true
	default:
		return false
	}
}

// Decide answers whether one entity's URL may be crawled.
func Decide(e Entity, now time.Time) Decision {
	volatile := Volatile(e.Type)

	// A duplicate is never advertised, whatever else is true of it. This is
	// checked first because a duplicate with a prediction and a recent kickoff
	// passes every other test, and duplicates are what cost this property half
	// its index.
	if e.DuplicateOf != 0 {
		return Decision{Indexable: false, Reason: ReasonDuplicate, Volatile: volatile}
	}

	switch e.Type {
	case TypeFixture:
		return decideFixture(e, now, volatile)
	case TypeTeam, TypeLeague, TypeCountry:
		// Stable entities turn on content alone. A club page with no played
		// fixture has nothing to rank and noindexes itself; advertising it
		// would be the sitemap contradicting the page again.
		if !e.HasContent {
			return Decision{Indexable: false, Reason: ReasonThin, Volatile: volatile}
		}
		return Decision{Indexable: true, Volatile: volatile}
	case TypeMarket, TypeDateHub, TypeStatic:
		// Hand-maintained surfaces. They exist because someone decided they
		// should, so the only question is whether they are still wanted.
		return Decision{Indexable: true, Volatile: volatile}
	default:
		// An unrecognised type is a programming error, and the safe failure is
		// to withhold: a page missing from the index recovers on the next
		// sweep, a wrongly advertised one teaches Google to distrust the file.
		return Decision{Indexable: false, Reason: ReasonThin, Volatile: volatile}
	}
}

func decideFixture(e Entity, now time.Time, volatile bool) Decision {
	// No prediction, nothing to rank. This is the largest excluded population
	// by far -- 5,580 of 12,903 fixtures in the indexable window.
	if !e.HasPrediction {
		return Decision{Indexable: false, Reason: ReasonNoPrediction, Volatile: volatile}
	}

	// Archived is checked BEFORE the history gate, and the order carries
	// meaning. Both exclude, but only one is recoverable: a fixture withheld
	// for thin history becomes indexable as its teams play, while an archived
	// one never does. Reporting the recoverable reason on a fixture that is
	// really just old would inflate the very population the history gate exists
	// to measure.
	if archivedFixture(e, now) {
		return Decision{Indexable: false, Reason: ReasonArchived, Volatile: volatile}
	}

	// An unsupplied count cannot judge anything; see Entity.HistoryKnown.
	if e.HistoryKnown && !HasIndexableHistory(e.HomeHistory, e.AwayHistory) {
		return Decision{Indexable: false, Reason: ReasonInsufficientHistory, Volatile: volatile}
	}

	return Decision{Indexable: true, Volatile: volatile}
}

// archivedFixture reports whether a fixture has aged out of search demand.
//
// Extracted from decideFixture so the history gate can run after it without
// duplicating the lifecycle, and so the several "not archived" cases stay
// legible as one question rather than a chain of early returns that each had
// to remember to answer `Indexable: true`.
func archivedFixture(e Entity, now time.Time) bool {
	status := strings.ToLower(strings.TrimSpace(e.Status))

	// A match being played is never archived, however old its kickoff reads.
	if inPlayStatuses[status] {
		return false
	}

	// An unknown kickoff cannot be aged. Losing a page that should rank is the
	// worse error here: the fixture is still reachable and still has a
	// prediction, and the next sweep re-decides once the data is repaired.
	if e.KickoffAt.IsZero() {
		return false
	}

	// Ahead of kickoff there is nothing to archive.
	if e.KickoffAt.After(now) {
		return false
	}

	// Past kickoff but not concluded: treat as still in progress rather than
	// as an aged-out fixture. Includes `unknown`, which means the reaper gave
	// up on a match, not that the match is finished.
	if !concludedStatuses[status] {
		return false
	}

	// Fractional days, not whole ones. Rounding to days here is the exact bug
	// that advertised a 14.16-day-old fixture the page had already archived.
	return now.Sub(e.KickoffAt).Hours()/24.0 > SettledIndexableDays
}

// HasIndexableHistory reports whether a fixture has enough settled match
// history behind it to be worth advertising.
//
// Mirrors hasIndexableHistory in apps/web/app/lib/fixtureLifecycle.ts. The page
// decides its own robots tag, and a sitemap that disagrees advertises a
// noindexed URL -- the exact class of defect this package exists to prevent --
// so the two implementations are pinned together by
// TestGoAndTypeScriptFixtureHistoryAgree.
//
// BOTH sides must clear the bar, not the pair on average. A fixture where one
// club has forty matches on record and its opponent has none still produces a
// preview that can say nothing about half the match.
func HasIndexableHistory(homeHistory, awayHistory int) bool {
	return homeHistory >= MinimumFixtureHistory && awayHistory >= MinimumFixtureHistory
}

// ValidPath reports whether a path may be stored at all.
//
// The table's primary key is the path, so a malformed one is not a cosmetic
// problem: it is a row that collides, or that the sitemap renders into invalid
// XML, or that advertises a URL nobody can fetch. Rejecting at the boundary is
// cheaper than discovering it in a crawler's error report.
func ValidPath(path string) bool {
	if path == "" || len(path) > 2000 {
		return false
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	// A host in the path means somebody concatenated a URL where a path was
	// wanted, which yields https://sokalens.com/https://sokalens.com/...
	if strings.Contains(path, "://") {
		return false
	}
	// Locale prefixes are the duplication #96 removed. A path carrying one is
	// a second URL for content that already has a canonical home.
	for _, locale := range []string{"/en/", "/es/", "/fr/", "/pt/", "/ar/", "/de/", "/it/"} {
		if strings.HasPrefix(path, locale) {
			return false
		}
	}
	if strings.HasPrefix(path, "//") {
		return false
	}
	// Whitespace and control characters break the XML the sitemap renders.
	if strings.ContainsAny(path, " \t\r\n<>\"'") {
		return false
	}
	return true
}
