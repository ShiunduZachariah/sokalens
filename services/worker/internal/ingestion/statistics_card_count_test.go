package ingestion

import (
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// card_points is cards shown (yellow + red), the unit the engine forecasts
// and settles total_cards on. It was yellow*10 + red*25 here, which settled
// "over 5.5" as won from a single booking.
func TestStatRecordCardPointsCountsCardsShown(t *testing.T) {
	cases := []struct {
		name  string
		items []apifootball.StatisticItem
		want  *int
	}{
		{"yellows and a red", []apifootball.StatisticItem{{Type: "Yellow Cards", Value: "2"}, {Type: "Red Cards", Value: "1"}, {Type: "Fouls", Value: "12"}}, intPtr(3)},
		{"one yellow is one card, not ten", []apifootball.StatisticItem{{Type: "Yellow Cards", Value: "1"}, {Type: "Fouls", Value: "9"}}, intPtr(1)},
		{"a lone red", []apifootball.StatisticItem{{Type: "Red Cards", Value: "1"}}, intPtr(1)},
		// API-Football reports zero as null: a captured row with no card
		// entries is a clean match.
		{"captured row without cards", []apifootball.StatisticItem{{Type: "Fouls", Value: "11"}, {Type: "Corner Kicks", Value: "4"}}, intPtr(0)},
		{"nothing captured", []apifootball.StatisticItem{{Type: "Ball Possession", Value: "55%"}}, nil},
		{"empty", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := statRecordFromItems(tc.items).CardPoints
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("card_points = %d, want nil", *got)
			case tc.want != nil && got == nil:
				t.Fatalf("card_points = nil, want %d", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Fatalf("card_points = %d, want %d", *got, *tc.want)
			}
		})
	}
}
