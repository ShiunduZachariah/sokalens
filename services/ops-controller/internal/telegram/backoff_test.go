package telegram

import (
	"testing"
	"time"
)

func constantJitter(f float64) func() float64 { return func() float64 { return f } }

func TestBackoffGrowsFromFloorAndSaturatesAtCeiling(t *testing.T) {
	b := NewBackoffWithJitter(time.Second, 5*time.Minute, constantJitter(0))

	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
		64 * time.Second,
		128 * time.Second,
		256 * time.Second,
		5 * time.Minute,
		5 * time.Minute,
		5 * time.Minute,
	}
	for i, expected := range want {
		if got := b.Next(); got != expected {
			t.Fatalf("Next call %d = %s, want %s", i+1, got, expected)
		}
	}
}

func TestBackoffResetReturnsToFloor(t *testing.T) {
	b := NewBackoffWithJitter(time.Second, 5*time.Minute, constantJitter(0))
	for i := 0; i < 6; i++ {
		b.Next()
	}
	b.Reset()
	if got := b.Next(); got != time.Second {
		t.Fatalf("after Reset, Next = %s, want %s", got, time.Second)
	}
}

func TestBackoffJitterStaysWithinBounds(t *testing.T) {
	const (
		floor   = time.Second
		ceiling = 5 * time.Minute
	)
	b := newBackoff(floor, ceiling)

	base := floor
	for i := 0; i < 12; i++ {
		got := b.Next()
		upper := base + time.Duration(float64(base)*jitterSpread)
		if upper > ceiling {
			upper = ceiling
		}
		if got < base || got > upper {
			t.Fatalf("Next call %d = %s, want within [%s, %s]", i+1, got, base, upper)
		}
		if got < floor || got > ceiling {
			t.Fatalf("Next call %d = %s escaped [%s, %s]", i+1, got, floor, ceiling)
		}
		if base *= 2; base > ceiling {
			base = ceiling
		}
	}
}

func TestBackoffJitterUpperBound(t *testing.T) {
	b := NewBackoffWithJitter(time.Second, 5*time.Minute, constantJitter(1))

	if got, want := b.Next(), 1500*time.Millisecond; got != want {
		t.Fatalf("first Next = %s, want %s", got, want)
	}
	if got, want := b.Next(), 3*time.Second; got != want {
		t.Fatalf("second Next = %s, want %s", got, want)
	}
}

func TestBackoffClampsMisbehavingJitterSource(t *testing.T) {
	for _, fraction := range []float64{-5, 5} {
		b := NewBackoffWithJitter(time.Second, 4*time.Second, constantJitter(fraction))
		got := b.Next()
		if got < time.Second || got > 1500*time.Millisecond {
			t.Fatalf("jitter %v produced %s, want within [1s, 1.5s]", fraction, got)
		}
	}
}

func TestBackoffRejectsDegenerateBounds(t *testing.T) {
	b := NewBackoffWithJitter(0, 0, nil)
	if got := b.Next(); got != time.Second {
		t.Fatalf("degenerate bounds produced %s, want the %s default floor", got, time.Second)
	}

	inverted := NewBackoffWithJitter(10*time.Second, time.Second, constantJitter(0))
	if got := inverted.Next(); got != 10*time.Second {
		t.Fatalf("inverted bounds produced %s, want %s", got, 10*time.Second)
	}
	if got := inverted.Next(); got != 10*time.Second {
		t.Fatalf("inverted bounds second call produced %s, want %s", got, 10*time.Second)
	}
}

func TestBackoffConcurrentUse(t *testing.T) {
	b := newBackoff(time.Second, 5*time.Minute)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 50; j++ {
				b.Next()
				b.Reset()
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
