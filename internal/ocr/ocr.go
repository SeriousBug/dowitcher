// Package ocr recognises text in page images with Tesseract.
//
// Tesseract runs as a WebAssembly module under wazero rather than as a linked C
// library, which is what keeps CGO_ENABLED=0 and the distroless/static image
// intact — the same reason internal/cbz decodes AVIF through WASM. A CGO
// Tesseract binding would also force operators to install the tesseract system
// package, which a single-binary deployment cannot ask for.
//
// English training data is embedded so OCR works with no operator setup at all.
// The tessdata_fast variant is used: it is a few megabytes against tessdata's
// tens, and the accuracy difference on printed comic lettering does not justify
// the binary size. An operator who needs another language, or the more accurate
// data, sets DOWITCHER_TESSDATA to a .traineddata file, which reaches New as the
// path argument.
package ocr

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/SeriousBug/dowitcher/internal/gogosseract"
	"github.com/tetratelabs/wazero"
)

//go:embed tessdata/eng.traineddata
var embeddedTrainingData []byte

// Engine holds the training data and serialises recognition.
//
// The serialisation is the point, not an implementation detail: recognition is
// the most expensive thing this server does, and the WASM module is
// single-threaded, so one call already saturates one core. Without the mutex,
// two concurrent MCP calls would each stand up their own Tesseract instance and
// between them occupy two cores and twice the memory. One at a time keeps OCR to
// a single CPU no matter how many agents ask at once.
type Engine struct {
	mu   sync.Mutex
	data []byte
	// cache lets the second and later calls skip compiling the Tesseract module.
	// The instance itself is built and torn down per batch so an idle server
	// holds no Tesseract memory; the compiled module is the expensive part and
	// it is the part worth keeping.
	cache wazero.CompilationCache
}

// New returns an engine using the training data at path, or the embedded English
// data when path is empty. A path that cannot be read is an error rather than a
// fallback: an operator who named a file meant it, and silently OCRing in the
// wrong language would look like Tesseract doing a bad job.
func New(path string) (*Engine, error) {
	data := embeddedTrainingData
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read training data: %w", err)
		}
		if len(b) == 0 {
			return nil, fmt.Errorf("training data %s is empty", path)
		}
		data = b
	}
	return &Engine{data: data, cache: wazero.NewCompilationCache()}, nil
}

// Text recognises the text in each image, in order, and returns one string per
// image. Images are encoded page bytes (JPEG or PNG); the caller is responsible
// for having scaled them down to something Tesseract can hold.
//
// perImage bounds each recognition separately so one pathological scan costs its
// own timeout rather than the whole batch's. A timed-out image yields empty text
// instead of failing the batch: the other pages were recognised and are worth
// returning.
func (e *Engine) Text(ctx context.Context, images [][]byte, perImage time.Duration) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := gogosseract.Config{
		Language:     "eng",
		TrainingData: bytes.NewReader(e.data),
		WASMCache:    e.cache,
	}
	// Tesseract narrates its confidence on stderr for every image. That is not
	// this server's log.
	cfg.Stderr = io.Discard
	cfg.Stdout = io.Discard

	t, err := gogosseract.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("start tesseract: %w", err)
	}
	// Closing tears down the wazero runtime, which is what actually releases the
	// module's linear memory. A detached context, because the batch's own context
	// may already be done and the teardown still has to run.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		t.Close(closeCtx)
	}()

	out := make([]string, len(images))
	for i, img := range images {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		text, err := recognize(ctx, t, img, perImage)
		if err != nil {
			return out, err
		}
		out[i] = text
	}
	return out, nil
}

func recognize(ctx context.Context, t *gogosseract.Tesseract, img []byte, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := t.LoadImage(ctx, bytes.NewReader(img), gogosseract.LoadImageOptions{}); err != nil {
		return "", fmt.Errorf("load image: %w", err)
	}
	text, err := t.GetText(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("recognise text: %w", err)
	}
	return text, nil
}
