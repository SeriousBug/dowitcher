package imports

import (
	"context"
	"runtime"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/SeriousBug/dowitcher/internal/api"
)

const (
	aspectTolerance  = 0.01
	defaultThreshold = 3.0
)

// unionFind clusters representatives. Clustering is transitive by construction:
// A~B and B~C puts A, B and C in one group even when A vs C is over the
// threshold. That is intentional — a rescale chain drifts past the threshold
// end to end while every adjacent step stays under it.
type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p}
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}

// union reports whether it merged two distinct sets.
func (u *unionFind) union(a, b int) bool {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return false
	}
	u.parent[rb] = ra
	return true
}

// pageGroup is one cluster: every file that is the same artwork, in collected
// order, plus the one that survives into the CBZ.
type pageGroup struct {
	members []int // file indices, ascending
	keep    int   // file index of the kept member
	exact   bool  // every member shares one digest
}

type groupStats struct {
	exactDupes int
	nearDupes  int
}

// groupPages clusters files by visual content and picks a keeper per cluster.
func groupPages(ctx context.Context, files []*srcFile, in *ingested, threshold float64, progress ProgressFunc) ([]pageGroup, groupStats, error) {
	// Representatives are the distinct digests that decoded, ordered by their
	// earliest file so the pair sweep and its reporting are deterministic.
	var reps []*content
	for _, c := range in.byDigest {
		if c.decodeErr == nil && c.thumb != nil {
			reps = append(reps, c)
		}
	}
	slices.SortFunc(reps, func(a, b *content) int { return cmpInt(a.files[0], b.files[0]) })

	u := newUnionFind(len(reps))
	near, err := sweepPairs(ctx, reps, threshold, u, progress)
	if err != nil {
		return nil, groupStats{}, err
	}

	// Expand each component back to every file carrying any of its digests, so
	// a group holds the complete file set rather than just the representatives.
	components := make(map[int][]*content)
	for i, c := range reps {
		r := u.find(i)
		components[r] = append(components[r], c)
	}

	groups := make([]pageGroup, 0, len(components))
	for _, members := range components {
		var g pageGroup
		for _, c := range members {
			g.members = append(g.members, c.files...)
		}
		slices.Sort(g.members)
		g.exact = len(members) == 1
		g.keep = pickKeeper(files, in, g.members)
		groups = append(groups, g)
	}

	// Page order follows each group's EARLIEST member, not the file that
	// survives. package.py's reason: the hi-res copy trails its previews, so
	// ordering by the kept file would reverse posts that bundle two pages.
	slices.SortFunc(groups, func(a, b pageGroup) int { return cmpInt(a.members[0], b.members[0]) })

	return groups, groupStats{exactDupes: in.exactDupes, nearDupes: near}, nil
}

// candidate is a pair whose MAE cleared the threshold, awaiting a serial union.
type candidate struct{ a, b int }

// sweepPairs runs the O(n^2) MAE comparison and returns the number of merges.
//
// package.py runs this loop serially and uses the union-find state to skip
// pairs already known to be in the same set. Union-find is not concurrency
// safe, so here the MAE work fans out across goroutines and the unions are
// applied by a single consumer. The tradeoff: the skip is gone, so pairs that
// package.py would never have measured get measured anyway.
//
// That costs work but changes nothing. The skip only ever elides edges that are
// redundant for connectivity, so the resulting partition — and therefore the
// merge count, which is len(reps) minus the component count — is identical
// either way. It stays O(n^2) regardless; parallelism is the only lever.
func sweepPairs(ctx context.Context, reps []*content, threshold float64, u *unionFind, progress ProgressFunc) (int, error) {
	n := len(reps)
	if n < 2 {
		progress(api.StageGrouping, 0, 0)
		return 0, nil
	}

	total := n * (n - 1) / 2
	progress(api.StageGrouping, 0, total)

	cands := make(chan candidate, 1024)
	near := 0
	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		for c := range cands {
			if u.union(c.a, c.b) {
				near++
			}
		}
	}()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	var mu sync.Mutex
	done := 0

	// Rows are chunked so progress and the counter are not contended per pair.
	// Row i does n-1-i comparisons, so chunks are sized by row rather than
	// evenly to keep the tail from landing on one worker.
	for i := 0; i < n-1; i++ {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			var local []candidate
			for j := i + 1; j < n; j++ {
				a, b := reps[i], reps[j]
				if !aspectCompatible(a.dims, b.dims) {
					continue
				}
				if mae(a.thumb, b.thumb) <= threshold {
					local = append(local, candidate{i, j})
				}
			}
			for _, c := range local {
				select {
				case cands <- c:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
			mu.Lock()
			done += n - 1 - i
			progress(api.StageGrouping, done, total)
			mu.Unlock()
			return nil
		})
	}
	err := g.Wait()
	close(cands)
	consumer.Wait()
	if err != nil {
		return 0, err
	}
	return near, ctx.Err()
}

// pickKeeper keeps the highest-resolution member, breaking ties on file size.
//
// members is ascending, and the comparison is strict, so ties resolve to the
// earliest file — matching Python's max(), which returns the first maximal
// element.
func pickKeeper(files []*srcFile, in *ingested, members []int) int {
	best := members[0]
	bestPix, bestSize := pixelsOf(in, best), files[best].size
	for _, m := range members[1:] {
		pix, size := pixelsOf(in, m), files[m].size
		if pix > bestPix || (pix == bestPix && size > bestSize) {
			best, bestPix, bestSize = m, pix, size
		}
	}
	return best
}

func pixelsOf(in *ingested, idx int) int64 {
	c := in.byDigest[in.digestOf[idx]]
	if c == nil {
		return 0
	}
	return int64(c.dims.X) * int64(c.dims.Y)
}

// groupExact clusters on SHA-256 alone, skipping every pixel comparison.
//
// package.py's --exact keeps the first file of each digest and reports nothing
// about what it dropped. The keeper matches, but the siblings are carried along
// here so the dupe report can name them; they are byte-identical to the keeper,
// so nothing about the output bytes changes.
func groupExact(files []*srcFile, in *ingested) ([]pageGroup, groupStats) {
	groups := make([]pageGroup, 0, len(in.byDigest))
	for _, c := range in.byDigest {
		groups = append(groups, pageGroup{
			members: slices.Clone(c.files),
			keep:    c.files[0],
			exact:   true,
		})
	}
	slices.SortFunc(groups, func(a, b pageGroup) int { return cmpInt(a.members[0], b.members[0]) })
	return groups, groupStats{exactDupes: in.exactDupes}
}
