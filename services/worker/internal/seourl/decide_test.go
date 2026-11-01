package seourl

import (
	"testing"
	"time"
)

var now = time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC)

func fixture(mod func(*Entity)) Entity {
	e := Entity{
		Path:          "/predictions/premier-league/arsenal-vs-chelsea-123",
		Type:          TypeFixture,
		ID:            123,
		KickoffAt:     now.Add(-2 * time.Hour),
		Status:        "finished",
		HasPrediction: true,
	}
	if mod != nil {
		mod(&e)
	}
	return e
}

// ---------------------------------------------------------------------------
// The archive boundary. This is where the real defect was, so it gets the most
// attention: the sitemap advertised a 14.16-day-old fixture that the page had
// already archived, because one side counted whole days and the other hours.
// ---------------------------------------------------------------------------

func TestTheArchiveBoundaryIsFractional(t *testing.T) {
	cases := []struct {
		name      string
		ageHours  float64
		indexable bool
	}{
		{"a day inside", 13 * 24, true},
		{"one hour inside", 14*24 - 1, true},
		{"exactly fourteen days", 14 * 24, true}, // strictly greater archives
		{"one minute past", 14*24 + 0.0167, false},
		{"the 14.16 days that shipped the bug", 14.16 * 24, false},
		{"a month past", 30 * 24, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(func(e *Entity) {
				e.KickoffAt = now.Add(-time.Duration(tc.ageHours * float64(time.Hour)))
			})
			got := Decide(e, now)
			if got.Indexable != tc.indexable {
				t.Fatalf("age %.2fh: indexable = %v, want %v (reason %q)", tc.ageHours, got.Indexable, tc.indexable, got.Reason)
			}
			if !tc.indexable && got.Reason != ReasonArchived {
				t.Fatalf("age %.2fh: reason = %q, want archived", tc.ageHours, got.Reason)
			}
		})
	}
}

func TestAnArchivedFixtureIsNeverAdvertisedHoweverGoodItLooks(t *testing.T) {
	// Recent modification, a current prediction, a real path -- and still old.
	// Nothing about a page's quality outranks its age.
	e := fixture(func(e *Entity) {
		e.KickoffAt = now.AddDate(0, 0, -400)
		e.LastModified = now
	})
	if Decide(e, now).Indexable {
		t.Fatal("a fixture from last year was advertised")
	}
}

// ---------------------------------------------------------------------------
// Negative cases: the things that must never be advertised.
// ---------------------------------------------------------------------------

func TestAFixtureWithNoPredictionIsNeverIndexable(t *testing.T) {
	// The largest excluded population: 5,580 of 12,903 fixtures in the window.
	// These pages noindex themselves, so advertising them is the sitemap
	// contradicting the page -- how a property teaches Google to distrust it.
	for _, status := range []string{"scheduled", "live", "finished", "postponed", "unknown", ""} {
		e := fixture(func(e *Entity) {
			e.HasPrediction = false
			e.Status = status
		})
		got := Decide(e, now)
		if got.Indexable {
			t.Fatalf("status %q: advertised a fixture with no prediction", status)
		}
		if got.Reason != ReasonNoPrediction {
			t.Fatalf("status %q: reason = %q, want no_prediction", status, got.Reason)
		}
	}
}

func TestADuplicateIsNeverIndexableEvenWhenItPassesEveryOtherTest(t *testing.T) {
	// Duplicates are checked before anything else on purpose. A duplicate with
	// a fresh prediction and a recent kickoff satisfies every other rule, and
	// self-duplication is what cost this property half its index: Google
	// declined 8,318 of ~16,930 URLs and impressions fell 928/day to 6/day.
	e := fixture(func(e *Entity) { e.DuplicateOf = 999 })
	got := Decide(e, now)
	if got.Indexable {
		t.Fatal("a duplicate fixture was advertised")
	}
	if got.Reason != ReasonDuplicate {
		t.Fatalf("reason = %q, want duplicate", got.Reason)
	}

	// And for a stable entity with content, which is the 705-duplicate-teams
	// case still open in #121.
	team := Entity{Path: "/football/teams/manchester-united", Type: TypeTeam, HasContent: true, DuplicateOf: 7}
	if Decide(team, now).Indexable {
		t.Fatal("a duplicate team page was advertised")
	}
}

func TestAThinStableEntityIsNotAdvertised(t *testing.T) {
	for _, typ := range []EntityType{TypeTeam, TypeLeague, TypeCountry} {
		e := Entity{Path: "/football/teams/nobody", Type: typ, HasContent: false}
		got := Decide(e, now)
		if got.Indexable {
			t.Fatalf("%s with no content was advertised", typ)
		}
		if got.Reason != ReasonThin {
			t.Fatalf("%s: reason = %q, want thin", typ, got.Reason)
		}
	}
}

func TestAnUnknownEntityTypeIsWithheldRatherThanAdvertised(t *testing.T) {
	// The safe direction for a programming error. A page missing from the
	// index recovers on the next sweep; a wrongly advertised one damages trust
	// in the whole sitemap and cannot be un-sent.
	e := Entity{Path: "/whatever", Type: EntityType("invented")}
	got := Decide(e, now)
	if got.Indexable {
		t.Fatal("an unrecognised entity type was advertised")
	}
}

func TestAnExcludedDecisionAlwaysCarriesAReason(t *testing.T) {
	// The database enforces this too, but a decision that violates it would
	// fail on insert at 3am rather than here. Every path that can return
	// not-indexable is exercised.
	cases := []Entity{
		fixture(func(e *Entity) { e.HasPrediction = false }),
		fixture(func(e *Entity) { e.KickoffAt = now.AddDate(0, 0, -30) }),
		fixture(func(e *Entity) { e.DuplicateOf = 1 }),
		{Path: "/x", Type: TypeTeam, HasContent: false},
		{Path: "/x", Type: EntityType("invented")},
	}
	for i, e := range cases {
		got := Decide(e, now)
		if !got.Indexable && got.Reason == ReasonNone {
			t.Fatalf("case %d: excluded with no reason", i)
		}
		if got.Indexable && got.Reason != ReasonNone {
			t.Fatalf("case %d: indexable but carries reason %q", i, got.Reason)
		}
	}
}

// ---------------------------------------------------------------------------
// Edge cases in the data itself.
// ---------------------------------------------------------------------------

func TestAnUnknownKickoffDoesNotDeleteThePage(t *testing.T) {
	// A zero timestamp is bad data, not an old fixture. Treating it as aged
	// would silently de-index every page whose kickoff failed to parse, and
	// the failure would look like a traffic decline rather than a bug.
	e := fixture(func(e *Entity) { e.KickoffAt = time.Time{} })
	if !Decide(e, now).Indexable {
		t.Fatal("a fixture with an unknown kickoff was de-indexed")
	}
}

func TestALiveMatchIsNeverArchivedHoweverStaleItsClock(t *testing.T) {
	// A match stuck in second_half for three hours is the defect matchstate
	// exists for, and it happens. De-indexing a live match because its
	// timestamp drifted would turn a clock bug into a traffic bug.
	for _, status := range []string{"live", "halftime", "second_half", "extra_time", "penalties", "break"} {
		e := fixture(func(e *Entity) {
			e.Status = status
			e.KickoffAt = now.AddDate(0, 0, -100)
		})
		if !Decide(e, now).Indexable {
			t.Fatalf("status %q: a live match was archived on age", status)
		}
	}
}

func TestUnknownStatusPastKickoffIsNotTreatedAsConcluded(t *testing.T) {
	// `unknown` means the reaper gave up on a match, not that the match
	// finished. Ageing it out would quietly retire pages for matches whose
	// results are still being reconciled.
	e := fixture(func(e *Entity) {
		e.Status = "unknown"
		e.KickoffAt = now.AddDate(0, 0, -20)
	})
	if !Decide(e, now).Indexable {
		t.Fatal("an unresolved match was archived as if concluded")
	}
}

func TestStatusMatchingIsCaseAndSpaceInsensitive(t *testing.T) {
	// Provider data is not tidy. " FT " must not be read as an unknown status
	// and quietly kept out of the archive forever.
	old := now.AddDate(0, 0, -30)
	for _, status := range []string{"FT", " ft ", "Finished", "  POSTPONED"} {
		e := fixture(func(e *Entity) {
			e.Status = status
			e.KickoffAt = old
		})
		if Decide(e, now).Indexable {
			t.Fatalf("status %q: a month-old concluded fixture stayed indexable", status)
		}
	}
}

func TestAFutureKickoffIsIndexable(t *testing.T) {
	e := fixture(func(e *Entity) {
		e.KickoffAt = now.Add(72 * time.Hour)
		e.Status = "scheduled"
	})
	if !Decide(e, now).Indexable {
		t.Fatal("an upcoming fixture with a prediction was withheld")
	}
}

func TestAKickoffFarInTheFutureIsStillIndexable(t *testing.T) {
	// Clock skew, or a provider publishing next season. Neither should
	// de-index a page, and neither should overflow the age arithmetic.
	e := fixture(func(e *Entity) { e.KickoffAt = now.AddDate(5, 0, 0) })
	if !Decide(e, now).Indexable {
		t.Fatal("a far-future fixture was withheld")
	}
}

// ---------------------------------------------------------------------------
// Volatility: the distinction that keeps the sweep affordable.
// ---------------------------------------------------------------------------

func TestOnlyTimeDependentEntitiesAreVolatile(t *testing.T) {
	// Manchester United will still be Manchester United next season, so a team
	// row does not need re-deriving every cycle. A fixture's answer changes
	// four times in three weeks, and the last change has no event behind it.
	//
	// Getting this wrong in the safe direction costs throughput; getting it
	// wrong in the other direction means archived fixtures stay advertised.
	for _, typ := range []EntityType{TypeFixture, TypeDateHub} {
		if !Volatile(typ) {
			t.Fatalf("%s must be volatile; its indexability changes with the clock alone", typ)
		}
	}
	for _, typ := range []EntityType{TypeTeam, TypeLeague, TypeCountry, TypeMarket, TypeStatic} {
		if Volatile(typ) {
			t.Fatalf("%s marked volatile; re-deriving 13,763 unchanged rows a cycle is how a sweep gets disabled", typ)
		}
	}
}

func TestDecisionCarriesVolatilitySoTheSweepCanBandRows(t *testing.T) {
	if !Decide(fixture(nil), now).Volatile {
		t.Fatal("a fixture decision did not report volatile")
	}
	team := Entity{Path: "/football/teams/arsenal", Type: TypeTeam, HasContent: true}
	if Decide(team, now).Volatile {
		t.Fatal("a team decision reported volatile")
	}
}

// ---------------------------------------------------------------------------
// Path validation. The path is the primary key, so a malformed one is a
// collision, invalid XML, or a URL nobody can fetch.
// ---------------------------------------------------------------------------

func TestValidPathAcceptsWhatTheSiteActuallyServes(t *testing.T) {
	for _, path := range []string{
		"/",
		"/predictions/today",
		"/predictions/premier-league/arsenal-vs-chelsea-123",
		"/football/teams/manchester-united",
		"/predictions/markets/over-2-5-goals",
	} {
		if !ValidPath(path) {
			t.Fatalf("rejected a real path: %q", path)
		}
	}
}

func TestValidPathRejectsLocalePrefixes(t *testing.T) {
	// The /en/ tree was byte-identical to the root and both self-canonicalised,
	// which is half of why Google concluded the property duplicates itself.
	// #96 removed it; storing one here would put it back.
	for _, path := range []string{"/en/predictions/today", "/es/football/teams/arsenal", "/fr/", "/de/x", "/it/x", "/pt/x", "/ar/x"} {
		if ValidPath(path) {
			t.Fatalf("accepted a locale-prefixed duplicate: %q", path)
		}
	}
}

func TestValidPathRejectsMalformedInput(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"relative":          "predictions/today",
		"absolute URL":      "https://sokalens.com/predictions/today",
		"embedded scheme":   "/redirect?to=https://evil.example",
		"protocol relative": "//evil.example/path",
		"space":             "/predictions/arsenal vs chelsea",
		"newline":           "/predictions/x\ninjected",
		"carriage return":   "/predictions/x\rinjected",
		"tab":               "/predictions/x\tinjected",
		"angle bracket":     "/predictions/<script>",
		"quote":             "/predictions/\"quoted\"",
	}
	for name, path := range cases {
		if ValidPath(path) {
			t.Fatalf("%s: accepted %q", name, path)
		}
	}
}

func TestValidPathRejectsAnAbsurdlyLongPath(t *testing.T) {
	// A primary key is not the place to discover an unbounded string.
	long := "/" + string(make([]byte, 4000))
	if ValidPath(long) {
		t.Fatal("accepted a 4000-character path")
	}
}

// ---------------------------------------------------------------------------
// Cross-language agreement. The threshold is duplicated into TypeScript, so
// only a test keeps the two honest.
// ---------------------------------------------------------------------------

func TestTheArchiveThresholdMatchesTheWebApp(t *testing.T) {
	if SettledIndexableDays != 14.0 {
		t.Fatalf("SettledIndexableDays = %v; apps/web/app/lib/fixtureLifecycle.ts says 14", SettledIndexableDays)
	}
}
