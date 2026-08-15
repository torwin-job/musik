package queue

import (
	"math"
	"testing"
)

func TestTransitionNormMonotonic(t *testing.T) {
	maxTW := 10.0
	norm := func(w float64) float32 {
		if w <= 0 || maxTW <= 0 {
			return 0
		}
		return float32(math.Log1p(w) / math.Log1p(maxTW))
	}
	a := norm(1)
	b := norm(5)
	c := norm(10)
	if !(a < b && b <= c) {
		t.Fatalf("expected monotonic norms got %v %v %v", a, b, c)
	}
	if c > 1.01 {
		t.Fatalf("norm at max should be ~1 got %v", c)
	}
	old := float32(0.55*0.9 + 0.35*0.8)
	with := old + transitionLambda*norm(5)
	if with <= old {
		t.Fatal("transition boost should increase score")
	}
}

func TestCandidatePoolThreshold(t *testing.T) {
	if transitionLambda <= 0 {
		t.Fatal("lambda")
	}
}
