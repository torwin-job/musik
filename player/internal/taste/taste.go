package taste

import (
	"sync"

	"github.com/torwin-job/musik/player/internal/index"
)

// Maturity stages for cold-start → ready taste.
const (
	StatusDiscovering = "discovering"
	StatusForming     = "forming"
	StatusReady       = "ready"
)

type Profile struct {
	mu        sync.RWMutex
	Vec       []float32
	NPositive int
	NNegative int
	Source    string // online_ema | offline_snapshot | centroid
}

func New() *Profile { return &Profile{Source: "empty"} }

func (p *Profile) Set(v []float32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Vec = append([]float32(nil), v...)
	index.Normalize(p.Vec)
}

func (p *Profile) SetWithMeta(v []float32, nPos, nNeg int, source string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Vec = append([]float32(nil), v...)
	index.Normalize(p.Vec)
	p.NPositive = nPos
	p.NNegative = nNeg
	if source != "" {
		p.Source = source
	}
}

func (p *Profile) Get() []float32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.Vec) == 0 {
		return nil
	}
	out := make([]float32, len(p.Vec))
	copy(out, p.Vec)
	return out
}

func (p *Profile) Ready() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.Vec) > 0
}

func (p *Profile) Counts() (pos, neg int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.NPositive, p.NNegative
}

func (p *Profile) SetCounts(pos, neg int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.NPositive = pos
	p.NNegative = neg
}

func (p *Profile) SourceName() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Source
}

func (p *Profile) SetSource(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Source = s
}

// Maturity returns discovering|forming|ready from positive signal count.
func (p *Profile) Maturity(formingAt, readyAt int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if readyAt < 1 {
		readyAt = 15
	}
	if formingAt < 1 {
		formingAt = 5
	}
	if p.NPositive >= readyAt {
		return StatusReady
	}
	if p.NPositive >= formingAt {
		return StatusForming
	}
	return StatusDiscovering
}

// EffectiveExplore blends base explore with discover boost by maturity.
func (p *Profile) EffectiveExplore(base, discover float64, formingAt, readyAt int) float64 {
	switch p.Maturity(formingAt, readyAt) {
	case StatusDiscovering:
		if discover > base {
			return discover
		}
		return base
	case StatusForming:
		return (base + discover) / 2
	default:
		return base
	}
}

// UpdateEMA applies online taste update from signed listen weight.
func (p *Profile) UpdateEMA(trackVec []float32, signedWeight, alpha float64) {
	if len(trackVec) == 0 || alpha <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if signedWeight > 0 {
		p.NPositive++
	} else if signedWeight < 0 {
		p.NNegative++
	}
	p.Source = "online_ema"
	if len(p.Vec) == 0 {
		p.Vec = make([]float32, len(trackVec))
		copy(p.Vec, trackVec)
		if signedWeight < 0 {
			for i := range p.Vec {
				p.Vec[i] = -p.Vec[i]
			}
		}
		index.Normalize(p.Vec)
		return
	}
	if len(p.Vec) != len(trackVec) {
		return
	}
	a := float32(alpha)
	sw := float32(signedWeight)
	for i := range p.Vec {
		p.Vec[i] = (1-a)*p.Vec[i] + a*sw*trackVec[i]
	}
	index.Normalize(p.Vec)
}

// WeightFromListen maps listened/duration + reason → signed weight for EMA.
func WeightFromListen(listened, duration float64, reason string) (signed float64, action string) {
	if duration <= 1e-6 {
		duration = 1
	}
	w := listened / duration
	if w < 0 {
		w = 0
	}
	if w > 1 {
		w = 1
	}
	switch reason {
	case "skipped":
		if w < 0.3 {
			return -(1 - w), "skip"
		}
		if w < 0.8 {
			return 0.3 * w, "partial"
		}
		return w, "finish"
	case "completed", "next":
		if w >= 0.8 {
			return w, "finish"
		}
		if w >= 0.3 {
			return 0.5 * w, "partial"
		}
		return 0.15 * w, "short"
	default:
		if w >= 0.8 {
			return w, "finish"
		}
		if w >= 0.3 {
			return 0.4 * w, "partial"
		}
		return 0, "ignore"
	}
}

func LikeWeight() float64    { return 2.0 }
func DislikeWeight() float64 { return -2.0 }
