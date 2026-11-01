package predictions

import (
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

// A market hub is a standing page about a market, not about a date.
//
// It served one calendar day because `predictionWindow` had no way to express
// anything else, so a reader arriving on Over 2.5 Goals saw only today's
// fixtures -- and the prediction explorer's "Day" grouping, which appears only
// when the slate spans more than one, could never appear on it.
func TestPredictionWindowWidensWithDays(t *testing.T) {
	source := NewSource(nil)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	start, end := source.predictionWindow(httpserver.PredictionFilter{Days: 7})
	if got := start.Format("2006-01-02"); got != "2026-05-14" {
		t.Fatalf("start = %s, want 2026-05-14", got)
	}
	// Exclusive end: seven days starting on the 14th ends at the 21st.
	if got := end.Format("2006-01-02"); got != "2026-05-21" {
		t.Fatalf("end = %s, want 2026-05-21", got)
	}
}

// Every caller that does not ask keeps exactly the window it had. 0 is what a
// zero-valued filter carries, and it has to mean one day rather than none.
func TestPredictionWindowDefaultsToASingleDay(t *testing.T) {
	source := NewSource(nil)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	for _, days := range []int{0, 1, -3} {
		start, end := source.predictionWindow(httpserver.PredictionFilter{Days: days})
		if end.Sub(start) != 24*time.Hour {
			t.Fatalf("days=%d gave a %v window, want 24h", days, end.Sub(start))
		}
	}
}

// The horizon widens from the chosen start, not from today, so a dated or
// relative request still anchors where the caller put it.
func TestPredictionWindowWidensFromTheChosenStart(t *testing.T) {
	source := NewSource(nil)
	source.now = func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) }

	start, end := source.predictionWindow(httpserver.PredictionFilter{Day: "tomorrow", Days: 3})
	if got := start.Format("2006-01-02"); got != "2026-05-15" {
		t.Fatalf("start = %s, want 2026-05-15", got)
	}
	if got := end.Format("2006-01-02"); got != "2026-05-18" {
		t.Fatalf("end = %s, want 2026-05-18", got)
	}

	start, end = source.predictionWindow(httpserver.PredictionFilter{Date: "2026-06-01", Days: 2})
	if got := start.Format("2006-01-02"); got != "2026-06-01" {
		t.Fatalf("dated start = %s, want 2026-06-01", got)
	}
	if got := end.Format("2006-01-02"); got != "2026-06-03" {
		t.Fatalf("dated end = %s, want 2026-06-03", got)
	}
}
