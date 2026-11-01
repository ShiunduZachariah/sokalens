package footballdata

import "testing"

// card_points is cards shown: yellow + red, the unit the engine settles
// total_cards on and the worker's statistics job writes. A red was once
// counted twice here while the worker used yellow*10 + red*25.
func TestCardPointsCountsCardsShown(t *testing.T) {
	two, one, zero := 2, 1, 0
	cases := []struct {
		name        string
		yellow, red *int
		want        *int
	}{
		{"yellows and a red", &two, &one, intPointer(3)},
		{"a red counts once", nil, &one, intPointer(1)},
		{"no reds recorded", &two, nil, intPointer(2)},
		{"clean match", &zero, &zero, intPointer(0)},
		{"no card data", nil, nil, nil},
	}
	for _, tc := range cases {
		got := cardPoints(tc.yellow, tc.red)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Fatalf("%s: cardPoints = %v, want %v", tc.name, deref(got), deref(tc.want))
		}
	}
}

func intPointer(value int) *int { return &value }

func deref(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
