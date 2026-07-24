package imports

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// swapEncodeForTest replaces the encode pass with one whose success or failure
// is decided by fn(limit), and returns a restore func. On success it returns
// placeholder paths, which the adaptive wrapper's caller discards in these tests.
func swapEncodeForTest(fn func(limit int) error) func() {
	prev := encodePagesImpl
	encodePagesImpl = func(_ context.Context, pages []*srcFile, _ string, _ int, _ string, limit int, _ ProgressFunc) ([]string, error) {
		if err := fn(limit); err != nil {
			return nil, err
		}
		return make([]string, len(pages)), nil
	}
	return func() { encodePagesImpl = prev }
}

func TestIsMemoryError(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"avif: out of memory", true},
		{"wasm: cannot allocate 512 MiB", true},
		{"failed to allocate encoder", true},
		{"out of bounds memory access", true},
		{"grow: memory size exceeded", true},
		{"OUT OF MEMORY", true},
		{"unsupported image format", false},
		{"decode png: invalid header", false},
		{"context canceled", false},
	} {
		if got := isMemoryError(errors.New(tc.msg)); got != tc.want {
			t.Errorf("isMemoryError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

func TestAutoEncodeConcurrencyBounds(t *testing.T) {
	cores := runtime.NumCPU()
	// A page so large no sane budget fits two of it at once collapses to serial.
	huge, _ := autoEncodeConcurrency(1 << 30)
	if huge < 1 || huge > cores {
		t.Errorf("huge page concurrency = %d, want within [1,%d]", huge, cores)
	}
	if huge != 1 {
		// Only meaningful when a budget is actually readable; unknown-budget hosts
		// default low anyway, which also satisfies the >=1 bound above.
		if _, ok := availableMemoryBytes(); ok {
			t.Errorf("a 1Gpx page must encode serially, got concurrency %d", huge)
		}
	}
	// A tiny page never exceeds the core count.
	small, reason := autoEncodeConcurrency(1)
	if small < 1 || small > cores {
		t.Errorf("small page concurrency = %d, want within [1,%d]", small, cores)
	}
	if reason == "" {
		t.Error("autoEncodeConcurrency returned an empty reason")
	}
}

func TestMaxEncodePixels(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.png")
	big := filepath.Join(dir, "big.png")
	writePNG(t, small, synth(40, 60, 1, 0))
	writePNG(t, big, synth(200, 300, 2, 0))

	pages := []*srcFile{{abs: small}, {abs: big}}
	if got := maxEncodePixels(pages); got != 200*300 {
		t.Errorf("maxEncodePixels = %d, want %d", got, 200*300)
	}

	// An already-efficient page holds no encoder arena, so it is excluded even
	// when it is the largest file present.
	avifPage := filepath.Join(dir, "huge.avif")
	if err := os.WriteFile(avifPage, []byte("not really avif"), 0o600); err != nil {
		t.Fatal(err)
	}
	pages = append(pages, &srcFile{abs: avifPage})
	if got := maxEncodePixels(pages); got != 200*300 {
		t.Errorf("maxEncodePixels with copy-through page = %d, want %d", got, 200*300)
	}
}

// fakeEncodeError makes the encoder fail deterministically the first failUntil
// times encodePages is entered, so the adaptive retry can be exercised without a
// real OOM. It swaps the package encoder for the duration of the test.
func TestEncodePagesAdaptiveRetriesOnMemoryError(t *testing.T) {
	dir := t.TempDir()
	for i := range 4 {
		writePNG(t, filepath.Join(dir, fmt.Sprintf("p%d.png", i)), synth(40, 60, int64(i+1), 0))
	}
	files, err := collect(dir)
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	sawLimits := []int{}
	restore := swapEncodeForTest(func(limit int) error {
		mu.Lock()
		sawLimits = append(sawLimits, limit)
		fail := limit > 1
		mu.Unlock()
		if fail {
			return errors.New("avif: out of memory")
		}
		return nil
	})
	defer restore()

	work := t.TempDir()
	_, err = encodePagesAdaptive(context.Background(), files, "avif", 70, work, 4, func(api.ImportStage, int, int) {})
	if err != nil {
		t.Fatalf("adaptive encode should have succeeded after backing off: %v", err)
	}
	if len(sawLimits) == 0 || sawLimits[len(sawLimits)-1] != 1 {
		t.Errorf("expected the pass to back off to concurrency 1, saw limits %v", sawLimits)
	}
	// 4 -> 2 -> 1: every step must be strictly smaller than the last.
	for i := 1; i < len(sawLimits); i++ {
		if sawLimits[i] >= sawLimits[i-1] {
			t.Errorf("concurrency did not shrink monotonically: %v", sawLimits)
			break
		}
	}
}

func TestEncodePagesAdaptiveDoesNotRetryOnOtherErrors(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "p.png"), synth(40, 60, 1, 0))
	files, err := collect(dir)
	if err != nil {
		t.Fatal(err)
	}

	var calls int
	var mu sync.Mutex
	restore := swapEncodeForTest(func(int) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return errors.New("unsupported image format")
	})
	defer restore()

	_, err = encodePagesAdaptive(context.Background(), files, "avif", 70, t.TempDir(), 4, func(api.ImportStage, int, int) {})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("want the non-memory error surfaced, got %v", err)
	}
	if calls != 1 {
		t.Errorf("a non-memory error must not retry, encoder ran %d times", calls)
	}
}
