package index

import (
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
)

type Meta struct {
	ID          int64
	Path        string
	Title       string
	Artist      string
	Album       string
	Duration    float64
	FileMD5     string
	CreatedAt   time.Time
	ArtworkPath string
	ClusterID   int
	Shown       int
	SkipEarly   int
	Completed   int
}

type Index struct {
	mu     sync.RWMutex
	IDs    []int64
	Meta   []Meta
	Matrix []float32 // row-major N*D
	N      int
	D      int
	idRow  map[int64]int
	// Clone lookups rebuilt on Load — excludeTrack uses these, not a full scan.
	md5ToIDs     map[string][]int64
	songKeyToIDs map[string][]int64
	cfg          config.Config
}

func New(cfg config.Config) *Index {
	return &Index{
		cfg:          cfg,
		idRow:        map[int64]int{},
		md5ToIDs:     map[string][]int64{},
		songKeyToIDs: map[string][]int64{},
	}
}

// SongKey normalizes artist|title for clone grouping.
func SongKey(artist, title string) string {
	a := strings.ToLower(strings.TrimSpace(artist))
	t := strings.ToLower(strings.TrimSpace(title))
	if a == "" || t == "" {
		return ""
	}
	return a + "|" + t
}

func (idx *Index) Load(rows []db.TrackRow) error {
	if len(rows) == 0 {
		idx.mu.Lock()
		idx.IDs, idx.Meta, idx.Matrix = nil, nil, nil
		idx.N, idx.D = 0, 0
		idx.idRow = map[int64]int{}
		idx.md5ToIDs = map[string][]int64{}
		idx.songKeyToIDs = map[string][]int64{}
		idx.mu.Unlock()
		return nil
	}
	dim := rows[0].Dim
	if dim <= 0 {
		dim = len(rows[0].Embedding) / 4
	}
	n := len(rows)
	mat := make([]float32, n*dim)
	ids := make([]int64, n)
	meta := make([]Meta, n)
	idRow := make(map[int64]int, n)
	md5ToIDs := make(map[string][]int64)
	songKeyToIDs := make(map[string][]int64)

	for i, r := range rows {
		d := r.Dim
		if d <= 0 {
			d = len(r.Embedding) / 4
		}
		if d != dim {
			return fmt.Errorf("inconsistent embedding dim: %d vs %d", d, dim)
		}
		vec := BytesToFloat32(r.Embedding)
		if len(vec) < dim {
			return fmt.Errorf("short embedding track %d", r.ID)
		}
		Normalize(vec[:dim])
		copy(mat[i*dim:(i+1)*dim], vec[:dim])
		ids[i] = r.ID
		created, _ := time.Parse(time.RFC3339Nano, r.CreatedAt)
		if created.IsZero() {
			created, _ = time.Parse(time.RFC3339, r.CreatedAt)
		}
		meta[i] = Meta{
			ID: r.ID, Path: r.Path, Title: r.Title, Artist: r.Artist, Album: r.Album,
			Duration: r.Duration, FileMD5: r.FileMD5, CreatedAt: created,
			ArtworkPath: r.ArtworkPath, ClusterID: r.ClusterID,
			Shown: r.Shown, SkipEarly: r.SkipEarly, Completed: r.Completed,
		}
		idRow[r.ID] = i
		if r.FileMD5 != "" {
			md5ToIDs[r.FileMD5] = append(md5ToIDs[r.FileMD5], r.ID)
		}
		if key := SongKey(r.Artist, r.Title); key != "" {
			songKeyToIDs[key] = append(songKeyToIDs[key], r.ID)
		}
	}

	idx.mu.Lock()
	idx.IDs, idx.Meta, idx.Matrix = ids, meta, mat
	idx.N, idx.D = n, dim
	idx.idRow = idRow
	idx.md5ToIDs = md5ToIDs
	idx.songKeyToIDs = songKeyToIDs
	idx.mu.Unlock()
	return nil
}

// CloneIDs returns trackID plus all MD5 / artist+title duplicates (deduped).
func (idx *Index) CloneIDs(trackID int64) []int64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	row, ok := idx.idRow[trackID]
	if !ok {
		return []int64{trackID}
	}
	m := idx.Meta[row]
	seen := map[int64]bool{trackID: true}
	out := []int64{trackID}
	add := func(ids []int64) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if m.FileMD5 != "" {
		add(idx.md5ToIDs[m.FileMD5])
	}
	if key := SongKey(m.Artist, m.Title); key != "" {
		add(idx.songKeyToIDs[key])
	}
	return out
}

func (idx *Index) Size() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.N
}

func (idx *Index) Dim() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.D
}

func (idx *Index) RowOf(id int64) (int, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	r, ok := idx.idRow[id]
	return r, ok
}

func (idx *Index) Vector(row int) []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N {
		return nil
	}
	out := make([]float32, idx.D)
	copy(out, idx.Matrix[row*idx.D:(row+1)*idx.D])
	return out
}

func (idx *Index) Centroid() []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.N == 0 {
		return nil
	}
	rows := make([]int, idx.N)
	for i := range rows {
		rows[i] = i
	}
	return idx.centroidOfLocked(rows)
}

// CentroidOf returns the L2-normalized mean embedding of the given rows.
func (idx *Index) CentroidOf(rows []int) []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.centroidOfLocked(rows)
}

func (idx *Index) centroidOfLocked(rows []int) []float32 {
	if idx.N == 0 || idx.D == 0 || len(rows) == 0 {
		return nil
	}
	sum := make([]float64, idx.D)
	n := 0
	for _, i := range rows {
		if i < 0 || i >= idx.N {
			continue
		}
		off := i * idx.D
		for d := 0; d < idx.D; d++ {
			sum[d] += float64(idx.Matrix[off+d])
		}
		n++
	}
	if n == 0 {
		return nil
	}
	out := make([]float32, idx.D)
	inv := 1.0 / float64(n)
	for d := 0; d < idx.D; d++ {
		out[d] = float32(sum[d] * inv)
	}
	Normalize(out)
	return out
}

// RowsForArtist returns index rows whose artist matches (case-insensitive trim).
func (idx *Index) RowsForArtist(artist string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	want := normName(artist)
	if want == "" {
		return nil
	}
	var rows []int
	for i, m := range idx.Meta {
		if normName(m.Artist) == want {
			rows = append(rows, i)
		}
	}
	return rows
}

// RowsForAlbum returns index rows for artist+album (album alone if artist empty).
func (idx *Index) RowsForAlbum(artist, album string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	wantA := normName(artist)
	wantAl := normName(album)
	if wantAl == "" {
		return nil
	}
	var rows []int
	for i, m := range idx.Meta {
		if normName(m.Album) != wantAl {
			continue
		}
		if wantA != "" && normName(m.Artist) != wantA {
			continue
		}
		rows = append(rows, i)
	}
	return rows
}

func normName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		return unicode.ToLower(r)
	}, s)
}

func (idx *Index) SimsTo(vec []float32) []float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]float32, idx.N)
	if idx.N == 0 || len(vec) != idx.D {
		return out
	}
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	// Parallel only pays off on larger libraries.
	if idx.N < 1500 || workers < 2 {
		for i := 0; i < idx.N; i++ {
			off := i * idx.D
			var s float32
			for d := 0; d < idx.D; d++ {
				s += idx.Matrix[off+d] * vec[d]
			}
			out[i] = s
		}
		return out
	}
	var wg sync.WaitGroup
	chunk := (idx.N + workers - 1) / workers
	for w := 0; w < workers; w++ {
		lo := w * chunk
		hi := lo + chunk
		if hi > idx.N {
			hi = idx.N
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				off := i * idx.D
				var s float32
				for d := 0; d < idx.D; d++ {
					s += idx.Matrix[off+d] * vec[d]
				}
				out[i] = s
			}
		}(lo, hi)
	}
	wg.Wait()
	return out
}

// Dot returns cosine/dot of row i with vec (vectors assumed L2-normalized).
func (idx *Index) Dot(row int, vec []float32) float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N || len(vec) != idx.D {
		return 0
	}
	off := row * idx.D
	var s float32
	for d := 0; d < idx.D; d++ {
		s += idx.Matrix[off+d] * vec[d]
	}
	return s
}

func (idx *Index) MetaAt(row int) Meta {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N {
		return Meta{}
	}
	return idx.Meta[row]
}

func (idx *Index) NewBoost(row int, now time.Time) float32 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if row < 0 || row >= idx.N {
		return 0
	}
	m := idx.Meta[row]
	if m.SkipEarly >= 3 {
		return 0
	}
	if m.CreatedAt.IsZero() {
		return 0
	}
	ageDays := now.Sub(m.CreatedAt).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	if ageDays > float64(idx.cfg.NewTrackDays) {
		return 0
	}
	tau := idx.cfg.NewBoostTauDays
	if tau < 1 {
		tau = 14
	}
	gamma := idx.cfg.NewBoostGamma
	if gamma < 1 {
		gamma = 5
	}
	boost := idx.cfg.NewBoostBeta * math.Exp(-ageDays/tau) * math.Exp(-float64(m.Shown)/gamma)
	return float32(boost)
}

func (idx *Index) BumpShownLocal(id int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.idRow[id]; ok {
		idx.Meta[r].Shown++
	}
}

func (idx *Index) BumpSkipEarlyLocal(id int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.idRow[id]; ok {
		idx.Meta[r].SkipEarly++
	}
}

func (idx *Index) BumpCompletedLocal(id int64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if r, ok := idx.idRow[id]; ok {
		idx.Meta[r].Completed++
	}
}

func BytesToFloat32(b []byte) []float32 {
	n := len(b) / 4
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(b[i*4 : (i+1)*4])
		out[i] = math.Float32frombits(bits)
	}
	return out
}

func Float32Bytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:(i+1)*4], math.Float32bits(f))
	}
	return b
}

func Normalize(v []float32) {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	s = math.Sqrt(s)
	if s < 1e-12 {
		return
	}
	inv := float32(1 / s)
	for i := range v {
		v[i] *= inv
	}
}

func Quantile(vals []float32, q float64) float32 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float32(nil), vals...)
	// simple insertion sort — N small enough for queue building pools
	for i := 1; i < len(cp); i++ {
		j := i
		for j > 0 && cp[j-1] > cp[j] {
			cp[j-1], cp[j] = cp[j], cp[j-1]
			j--
		}
	}
	pos := q * float64(len(cp)-1)
	i := int(pos)
	if i >= len(cp)-1 {
		return cp[len(cp)-1]
	}
	frac := float32(pos - float64(i))
	return cp[i]*(1-frac) + cp[i+1]*frac
}
