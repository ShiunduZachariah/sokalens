package acca

import (
	"math"
	"testing"
	"time"
)

func validTestSlip() Slip {
	now := time.Now().UTC().Add(time.Hour)
	return Slip{ID: "s1", LegCount: 2, CombinedFairOdds: 4.2, RankingScore: .8, EarliestKickoff: now, LatestKickoff: now.Add(time.Hour), Legs: []Leg{
		{MatchID: 1, Market: "1x2", Selection: "home", FairOdds: 1.8, KickoffAt: now},
		{MatchID: 2, Market: "btts", Selection: "yes", FairOdds: 2.3, KickoffAt: now.Add(time.Hour)},
	}}
}

func TestValidSlipRejectsNonFiniteAndZeroOdds(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Slip)
	}{
		{"combined infinity", func(s *Slip) { s.CombinedFairOdds = math.Inf(1) }},
		{"combined zero", func(s *Slip) { s.CombinedFairOdds = 0 }},
		{"leg infinity", func(s *Slip) { s.Legs[0].FairOdds = math.Inf(1) }},
		{"leg zero", func(s *Slip) { s.Legs[0].FairOdds = 0 }},
		{"ranking nan", func(s *Slip) { s.RankingScore = math.NaN() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validTestSlip()
			tc.mutate(&s)
			if validSlip(s) {
				t.Fatal("invalid slip accepted")
			}
		})
	}
}

func TestValidSlipAcceptsMixedMarketInventory(t *testing.T) {
	if !validSlip(validTestSlip()) {
		t.Fatal("valid mixed-market slip rejected")
	}
}

func TestSystemInventoryMustMeetLegAndScaledCombinedOddsFloors(t *testing.T) {
	slip := validTestSlip()
	slip.LegCount = 4
	slip.Legs = append(slip.Legs,
		Leg{MatchID: 3, Market: "1x2", Selection: "away", FairOdds: 1.2, KickoffAt: slip.EarliestKickoff},
		Leg{MatchID: 4, Market: "btts", Selection: "no", FairOdds: 1.2, KickoffAt: slip.EarliestKickoff},
	)
	slip.CombinedFairOdds = 2.0
	if validSlip(slip) {
		t.Fatal("system inventory accepted a 4-leg slip below its 2.25 floor")
	}

	slip.CombinedFairOdds = 2.25
	slip.Legs[0].FairOdds = 1.01
	if validSlip(slip) {
		t.Fatal("system inventory accepted a leg below 1.10")
	}

	// On-demand slips are explicitly user-controlled and retain flexible odds.
	slip.Origin = GeneratedOrigin
	if !validSlip(slip) {
		t.Fatal("user-controlled on-demand ACCA was incorrectly forced through the system inventory floor")
	}
}
