package notifications

import (
	"testing"
	"time"
)

func TestNextDeliveryAfterHandlesOvernightQuietHoursInUserTimezone(t *testing.T) {
	start := clockTime{hour: 22}
	end := clockTime{hour: 7}
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before local quiet end",
			now:  time.Date(2026, 8, 5, 2, 30, 0, 0, time.UTC), // 05:30 Nairobi
			want: time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC),  // 07:00 Nairobi
		},
		{
			name: "after local quiet start",
			now:  time.Date(2026, 8, 5, 20, 30, 0, 0, time.UTC), // 23:30 Nairobi
			want: time.Date(2026, 8, 6, 4, 0, 0, 0, time.UTC),   // 07:00 Nairobi next day
		},
		{
			name: "outside quiet hours",
			now:  time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := nextDeliveryAfter(test.now, "Africa/Nairobi", start, end)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("next delivery=%s, want %s", got, test.want)
			}
		})
	}
}

func TestNextDeliveryAfterHandlesSameDayQuietHours(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 30, 0, 0, time.UTC)
	got, err := nextDeliveryAfter(now, "UTC", clockTime{hour: 12}, clockTime{hour: 14})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("next delivery=%s, want %s", got, want)
	}
}

func TestNextDeliveryAfterRejectsInvalidTimezone(t *testing.T) {
	_, err := nextDeliveryAfter(time.Now(), "Invalid/SokaLens", clockTime{hour: 22}, clockTime{hour: 7})
	if err == nil {
		t.Fatal("nextDeliveryAfter returned nil error for invalid timezone")
	}
}
