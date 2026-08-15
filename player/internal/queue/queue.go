package queue

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/index"
)

const transitionLambda = float32(0.2)

type Item struct {
	TrackID     int64   `json:"track_id"`
	Artist      string  `json:"artist"`
	Title       string  `json:"title"`
	Album       string  `json:"album,omitempty"`
	Path        string  `json:"path"`
	Duration    float64 `json:"duration"`
	Score       float64 `json:"score"`
	CosineTaste float64 `json:"cosine_taste"`
	CosineCur   float64 `json:"cosine_current"`
	Explanation string  `json:"explanation"`
	Explore     bool    `json:"explore"`
	NewBoost    bool    `json:"new_boost"`
	ClusterID   int     `json:"cluster_id,omitempty"`
}

type Builder struct {
	Idx *index.Index
	Cfg config.Config
	Rng *rand.Rand
}

type cand struct {
	row     int
	score   float32
	taste   float32
	cur     float32
	boost   float32
	explore bool
}

type BuildOpts struct {
	ExploreRatio   float64
	Discover       bool // strong random/far, weak taste
	PreferCluster  int  // -1 = any; otherwise soft boost same cluster
	Size           int
	TransitionsFrom map[int64]float64 // toID → weight for edges from current
}

func NewBuilder(idx *index.Index, cfg config.Config) *Builder {
	return &Builder{Idx: idx, Cfg: cfg, Rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

func (b *Builder) Build(currentID int64, taste []float32, exclude map[int64]bool) []Item {
	return b.BuildOpts(currentID, taste, exclude, BuildOpts{})
}

func (b *Builder) BuildOpts(currentID int64, taste []float32, exclude map[int64]bool, opts BuildOpts) []Item {
	n := b.Idx.Size()
	if n == 0 {
		return nil
	}
	k := opts.Size
	if k < 1 {
		k = b.Cfg.QueueSize
	}
	if k < 1 {
		k = 5
	}
	exploreRatio := opts.ExploreRatio
	if exploreRatio <= 0 {
		exploreRatio = b.Cfg.ExploreRatio
	}
	if taste == nil {
		taste = b.Idx.Centroid()
	}

	now := time.Now()
	md5Cur := ""
	forbidden := map[int]bool{}
	curCluster := -999
	var curVec []float32
	if r, ok := b.Idx.RowOf(currentID); ok {
		forbidden[r] = true
		md5Cur = b.Idx.MetaAt(r).FileMD5
		curCluster = b.Idx.MetaAt(r).ClusterID
		curVec = b.Idx.Vector(r)
	}
	preferCluster := opts.PreferCluster
	if preferCluster == 0 {
		preferCluster = -1
	}

	var maxTW float64
	for _, w := range opts.TransitionsFrom {
		if w > maxTW {
			maxTW = w
		}
	}
	normTW := func(w float64) float32 {
		if w <= 0 || maxTW <= 0 {
			return 0
		}
		return float32(math.Log1p(w) / math.Log1p(maxTW))
	}

	var tasteSims, curSims []float32
	var rowAllowed []int
	poolAt := b.Cfg.CandidatePoolAt
	if poolAt < 1 {
		poolAt = 8000
	}
	if n < poolAt {
		tasteSims = b.Idx.SimsTo(taste)
		if curVec != nil {
			curSims = b.Idx.SimsTo(curVec)
		} else {
			curSims = tasteSims
		}
		rowAllowed = make([]int, n)
		for i := 0; i < n; i++ {
			rowAllowed[i] = i
		}
	} else {
		rowAllowed = b.candidateRowsFast(n, curCluster, taste, opts.TransitionsFrom, exclude, forbidden)
		tasteSims = make([]float32, n)
		curSims = make([]float32, n)
		for _, i := range rowAllowed {
			tasteSims[i] = b.Idx.Dot(i, taste)
			if curVec != nil {
				curSims[i] = b.Idx.Dot(i, curVec)
			} else {
				curSims[i] = tasteSims[i]
			}
		}
	}

	var nearPool, farPool []cand
	// quantile only over allowed rows when pooled
	threshSamples := make([]float32, 0, len(rowAllowed))
	for _, i := range rowAllowed {
		threshSamples = append(threshSamples, tasteSims[i])
	}
	thresh := index.Quantile(threshSamples, 0.25)
	for _, i := range rowAllowed {
		if forbidden[i] {
			continue
		}
		m := b.Idx.MetaAt(i)
		if exclude[m.ID] {
			continue
		}
		if md5Cur != "" && m.FileMD5 == md5Cur {
			continue
		}
		nb := b.Idx.NewBoost(i, now)
		tBoost := float32(0)
		if opts.TransitionsFrom != nil {
			tBoost = transitionLambda * normTW(opts.TransitionsFrom[m.ID])
		}
		var score float32
		if opts.Discover {
			score = 0.15*tasteSims[i] + 0.15*curSims[i] + nb + tBoost + float32(b.Rng.Float64())*0.7
		} else {
			score = 0.55*tasteSims[i] + 0.35*curSims[i] + nb + tBoost
			if preferCluster >= 0 && m.ClusterID == preferCluster {
				score += 0.05
			} else if preferCluster < 0 && curCluster >= 0 && m.ClusterID == curCluster {
				score += 0.03
			}
		}
		nearPool = append(nearPool, cand{
			row: i, score: score,
			taste: tasteSims[i], cur: curSims[i], boost: nb,
		})
		if tasteSims[i] <= thresh || opts.Discover {
			farScore := -tasteSims[i] + float32(b.Rng.Float64())*0.3
			if opts.Discover {
				farScore = float32(b.Rng.Float64())
			}
			farPool = append(farPool, cand{
				row: i, score: farScore, taste: tasteSims[i], cur: curSims[i], explore: true, boost: nb,
			})
		}
	}
	sort.Slice(nearPool, func(i, j int) bool { return nearPool[i].score > nearPool[j].score })
	sort.Slice(farPool, func(i, j int) bool { return farPool[i].score > farPool[j].score })

	nFar := int(math.Round(float64(k) * exploreRatio))
	if k >= 3 && exploreRatio > 0 && nFar < 1 {
		nFar = 1
	}
	if opts.Discover && nFar < k/2 {
		nFar = k / 2
	}
	if nFar > (k*2)/3 {
		nFar = (k * 2) / 3
	}
	nNear := k - nFar
	nNewCap := int(math.Ceil(float64(k) * 0.3))
	if opts.Discover {
		nNewCap = int(math.Ceil(float64(k) * 0.5))
	}

	used := map[int]bool{}
	artistCount := map[string]int{}
	usedMD5 := map[string]bool{}
	usedSong := map[string]bool{}
	if md5Cur != "" {
		usedMD5[md5Cur] = true
	}
	if r, ok := b.Idx.RowOf(currentID); ok {
		cm := b.Idx.MetaAt(r)
		sk := strings.ToLower(strings.TrimSpace(cm.Artist)) + "|" + strings.ToLower(strings.TrimSpace(cm.Title))
		if cm.Artist != "" && cm.Title != "" {
			usedSong[sk] = true
		}
	}
	newCount := 0

	take := func(src []cand, want int, explore bool) []cand {
		var out []cand
		for _, c := range src {
			if len(out) >= want {
				break
			}
			if used[c.row] {
				continue
			}
			m := b.Idx.MetaAt(c.row)
			if artistCount[m.Artist] >= 2 {
				continue
			}
			if m.FileMD5 != "" && usedMD5[m.FileMD5] {
				continue
			}
			sk := strings.ToLower(strings.TrimSpace(m.Artist)) + "|" + strings.ToLower(strings.TrimSpace(m.Title))
			if m.Artist != "" && m.Title != "" && usedSong[sk] {
				continue
			}
			isNew := c.boost > 0.01
			if isNew && newCount >= nNewCap {
				continue
			}
			c.explore = explore
			out = append(out, c)
			used[c.row] = true
			artistCount[m.Artist]++
			if m.FileMD5 != "" {
				usedMD5[m.FileMD5] = true
			}
			if m.Artist != "" && m.Title != "" {
				usedSong[sk] = true
			}
			if isNew {
				newCount++
			}
		}
		return out
	}

	near := take(nearPool, nNear, false)
	far := take(farPool, nFar, true)
	for _, c := range nearPool {
		if len(near)+len(far) >= k {
			break
		}
		if used[c.row] {
			continue
		}
		isNew := c.boost > 0.01
		if isNew && newCount >= nNewCap {
			continue
		}
		c.explore = false
		near = append(near, c)
		used[c.row] = true
		if isNew {
			newCount++
		}
	}
	for _, c := range nearPool {
		if len(near)+len(far) >= k {
			break
		}
		if used[c.row] {
			continue
		}
		c.explore = false
		c.boost = 0
		near = append(near, c)
		used[c.row] = true
	}

	ordered := interleave(near, far)
	out := make([]Item, 0, len(ordered))
	for _, c := range ordered {
		m := b.Idx.MetaAt(c.row)
		why := fmt.Sprintf("taste %.3f · current %.3f", c.taste, c.cur)
		if opts.Discover {
			why = fmt.Sprintf("discover · exploring library (%.3f)", c.taste)
		} else if c.explore {
			why = fmt.Sprintf("exploration · far from taste (%.3f)", c.taste)
		}
		if c.boost > 0.01 {
			why += fmt.Sprintf(" · new-boost +%.2f", c.boost)
		}
		if opts.TransitionsFrom != nil && opts.TransitionsFrom[m.ID] > 0 {
			why += " · transition"
		}
		out = append(out, Item{
			TrackID: m.ID, Artist: m.Artist, Title: m.Title, Album: m.Album, Path: m.Path,
			Duration: m.Duration, Score: float64(c.score),
			CosineTaste: float64(c.taste), CosineCur: float64(c.cur),
			Explanation: why, Explore: c.explore || opts.Discover && c.explore,
			NewBoost: c.boost > 0.01, ClusterID: m.ClusterID,
		})
	}
	return out
}

// candidateRowsFast builds a shortlist without a full N×D pass.
func (b *Builder) candidateRowsFast(
	n, curCluster int, taste []float32, transitions map[int64]float64,
	exclude map[int64]bool, forbidden map[int]bool,
) []int {
	seen := map[int]bool{}
	add := func(row int) {
		if row < 0 || row >= n || forbidden[row] || seen[row] {
			return
		}
		id := b.Idx.MetaAt(row).ID
		if exclude[id] {
			return
		}
		seen[row] = true
	}
	if curCluster >= 0 {
		for i := 0; i < n; i++ {
			if b.Idx.MetaAt(i).ClusterID == curCluster {
				add(i)
			}
		}
	}
	for toID := range transitions {
		if row, ok := b.Idx.RowOf(toID); ok {
			add(row)
		}
	}
	// random sample scored by taste → keep best
	type pair struct {
		row int
		sim float32
	}
	sampleN := 4000
	if sampleN > n {
		sampleN = n
	}
	sampled := make([]pair, 0, sampleN)
	for tries := 0; tries < sampleN*3 && len(sampled) < sampleN; tries++ {
		i := b.Rng.Intn(n)
		if forbidden[i] {
			continue
		}
		sampled = append(sampled, pair{i, b.Idx.Dot(i, taste)})
	}
	sort.Slice(sampled, func(i, j int) bool { return sampled[i].sim > sampled[j].sim })
	topK := 1500
	if topK > len(sampled) {
		topK = len(sampled)
	}
	for _, p := range sampled[:topK] {
		add(p.row)
	}
	for tries := 0; tries < 800 && len(seen) < topK+800; tries++ {
		add(b.Rng.Intn(n))
	}
	rows := make([]int, 0, len(seen))
	for r := range seen {
		rows = append(rows, r)
	}
	return rows
}

// PickRandom returns a random track id not in exclude.
func (b *Builder) PickRandom(exclude map[int64]bool) int64 {
	n := b.Idx.Size()
	if n == 0 {
		return 0
	}
	for tries := 0; tries < n*2; tries++ {
		i := b.Rng.Intn(n)
		id := b.Idx.MetaAt(i).ID
		if !exclude[id] {
			return id
		}
	}
	return b.Idx.MetaAt(0).ID
}

func interleave(near, far []cand) []cand {
	if len(far) == 0 {
		return near
	}
	out := make([]cand, 0, len(near)+len(far))
	gap := 2
	if len(near) > 0 {
		gap = max(2, len(near)/max(1, len(far)))
	}
	fi, since := 0, 0
	for _, c := range near {
		out = append(out, c)
		since++
		if fi < len(far) && since >= gap {
			out = append(out, far[fi])
			fi++
			since = 0
		}
	}
	for fi < len(far) {
		out = append(out, far[fi])
		fi++
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
