package geometry

import "testing"

// TestCenterBetaTrend checks the Feddersen secant correction starts near 1
// and rises (increasingly steeply) as a/W -> 1/2.
func TestCenterBetaTrend(t *testing.T) {
	g, err := New(CenterCrack, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if b := g.Beta(1e-9); b < 1 || b > 1.001 {
		t.Fatalf("center beta near a=0 = %v, want ~1", b)
	}
	prev := 0.0
	for r := 0.002; r < 0.5; r += 0.002 {
		b := g.Beta(r * 0.5)
		if b <= prev {
			t.Fatalf("center beta not increasing at r=%.3f: %v <= %v", r, b, prev)
		}
		prev = b
	}
	if b := g.Beta(0.49 * 0.5); b < 5 {
		t.Fatalf("center beta near limit = %v, want steep rise", b)
	}
}

// TestEdgeBetaTrend checks the Tada edge-crack correction sits at the
// free-surface factor ~1.12 for vanishing cracks and rises monotonically
// over the whole 0 < a/W < 1 range.
func TestEdgeBetaTrend(t *testing.T) {
	g, err := New(EdgeCrack, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	b0 := g.Beta(1e-9)
	if b0 < 1.11 || b0 > 1.13 {
		t.Fatalf("edge beta near a=0 = %v, want ~1.12", b0)
	}
	prev := 0.0
	for r := 0.001; r < 0.999; r += 0.001 {
		b := g.Beta(r * 0.5)
		if b < prev {
			t.Fatalf("edge beta not increasing at r=%.3f: %v < %v", r, b, prev)
		}
		prev = b
	}
	// Deep cracks see a much larger correction.
	if g.Beta(0.4*0.5) < b0 {
		t.Fatal("edge correction must grow with crack length")
	}
}

func TestNewRejectsBadWidth(t *testing.T) {
	if _, err := New(EdgeCrack, 0); err == nil {
		t.Fatal("expected error for zero width")
	}
	if _, err := New(Kind("other"), 0.1); err == nil {
		t.Fatal("expected error for unknown geometry")
	}
}

func TestCharacteristicConversions(t *testing.T) {
	ce, _ := New(EdgeCrack, 0.5)
	cc, _ := New(CenterCrack, 0.5)
	if ce.ToCharacteristic(0.02) != 0.02 {
		t.Fatal("edge characteristic should equal reported length")
	}
	if cc.ToCharacteristic(0.02) != 0.01 || cc.ToReported(0.01) != 0.02 {
		t.Fatal("center characteristic should be half the total crack length")
	}
	if ce.MaxCharacteristic() != 0.5 {
		t.Fatal("edge max is W")
	}
	if cc.MaxCharacteristic() != 0.25 {
		t.Fatal("center max is W/2")
	}
}
