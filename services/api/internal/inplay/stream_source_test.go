package inplay

import (
	"errors"
	"testing"
)

func TestDecodeMarketsReadsBasisPoints(t *testing.T) {
	got := decodeMarkets([]byte(`{"MATCH_RESULT":{"home":6412,"draw":2100,"away":1488}}`))
	if got["MATCH_RESULT"]["home"] != 6412 {
		t.Fatalf("markets = %v", got)
	}
	sum := 0
	for _, value := range got["MATCH_RESULT"] {
		sum += value
	}
	if sum != 10000 {
		t.Fatalf("selections sum to %d, want 10000", sum)
	}
}

func TestDecodeMarketsFailsSoftly(t *testing.T) {
	// One unreadable snapshot must not blank the series a client is drawing. The
	// point still carries its clock, score and trigger, so the line keeps its
	// shape with a gap in the numbers rather than vanishing.
	for name, raw := range map[string][]byte{
		"nil":          nil,
		"empty":        {},
		"garbage":      []byte(`not json`),
		"wrong shape":  []byte(`{"MATCH_RESULT":"home"}`),
		"empty object": []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			if got := decodeMarkets(raw); got != nil {
				t.Fatalf("decodeMarkets(%s) = %v, want nil", raw, got)
			}
		})
	}
}

func TestPgInt64ArrayRendersAPostgresLiteral(t *testing.T) {
	if got := pgInt64Array([]int64{3, 1, 2}); got != "{3,1,2}" {
		t.Fatalf("got %q", got)
	}
	if got := pgInt64Array(nil); got != "{}" {
		t.Fatalf("empty = %q, want {}", got)
	}
	// Negative ids cannot occur, but the rendering must not produce something
	// that changes the query's meaning if one ever did.
	if got := pgInt64Array([]int64{-1}); got != "{-1}" {
		t.Fatalf("got %q", got)
	}
}

// fakeRow replays one union row.
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *int64:
			*target = r.values[i].(int64)
		case *string:
			*target = r.values[i].(string)
		case *bool:
			*target = r.values[i].(bool)
		}
	}
	return nil
}

func TestScanChangeSurfacesAScanFailure(t *testing.T) {
	// A malformed row must not be skipped silently: a dropped change is a client
	// left permanently out of step, because its cursor moves past what it never
	// received.
	_, _, _, err := scanChange(fakeRow{err: errors.New("bad row")})
	if err == nil {
		t.Fatal("expected an error")
	}
}
