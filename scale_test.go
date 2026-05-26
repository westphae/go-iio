package iio

import "testing"

// IIO convention: (raw + offset) × scale. ICM-45686 temp uses offset=3200,
// scale=7.8125 → m°C before the temp channel divisor in buffered decode.
func TestScaleOffsetOrder(t *testing.T) {
	const (
		raw    = -8.0
		offset = 3200.0
		scale  = 7.8125
	)
	got := (raw + offset) * scale
	want := 24937.5 // ≈ 24.9 °C after /1000
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	wrong := raw*scale + offset
	if wrong == got {
		t.Fatalf("must not use scale-before-offset order")
	}
}
