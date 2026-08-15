package taste

import "testing"

func TestMaturity(t *testing.T) {
	p := New()
	p.SetCounts(0, 0)
	if p.Maturity(5, 15) != StatusDiscovering {
		t.Fatal("expected discovering")
	}
	p.SetCounts(5, 0)
	if p.Maturity(5, 15) != StatusForming {
		t.Fatal("expected forming")
	}
	p.SetCounts(15, 2)
	if p.Maturity(5, 15) != StatusReady {
		t.Fatal("expected ready")
	}
}

func TestWeightSkip(t *testing.T) {
	w, a := WeightFromListen(10, 200, "skipped")
	if w >= 0 || a != "skip" {
		t.Fatalf("got %v %s", w, a)
	}
}

func TestEffectiveExplore(t *testing.T) {
	p := New()
	p.SetCounts(0, 0)
	e := p.EffectiveExplore(0.25, 0.55, 5, 15)
	if e < 0.5 {
		t.Fatalf("discover explore too low: %v", e)
	}
	p.SetCounts(20, 0)
	e = p.EffectiveExplore(0.25, 0.55, 5, 15)
	if e != 0.25 {
		t.Fatalf("ready explore want 0.25 got %v", e)
	}
}
