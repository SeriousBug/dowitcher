package imports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// ErrNotPDF means the file did not open or parse as a PDF.
var ErrNotPDF = errors.New("imports: not a readable pdf")

// ErrPDFTooBig means the rasterised pages exceeded the byte budget: a PDF bomb
// whose page count or page size inflates the render past the upload cap.
var ErrPDFTooBig = errors.New("imports: rasterised pdf pages exceed the size cap")

// defaultPDFEncode is the page format a PDF import re-encodes to when the caller
// names none. A verbatim (empty) encode only made sense while the importer
// pulled original image bytes out of the PDF, where re-encoding would have
// thrown away quality for nothing. Rasterising already produces a fresh raster
// per page, so there is no original to preserve, and AVIF is a large size win
// over the intermediate JPEG for exactly this flat, full-colour content.
const defaultPDFEncode = "avif"

// renderMaxEdge caps the longer side of a rasterised page in pixels. Comic scans
// here are ~2700px on the long edge, so this reproduces native resolution
// without upscaling small pages into needless work, and bounds the pixels a
// single page can allocate well under the pipeline's decode limit.
const renderMaxEdge = 2600

// renderQuality is the JPEG quality of the intermediate page written for the
// pipeline. The pipeline re-encodes to the import's chosen format, so this only
// has to be high enough that the one extra generation is not visible.
const renderQuality = 92

// pdfiumInstanceTimeout bounds the wait for a free pdfium worker. A book holds
// its worker for the whole render, so this only bites when more concurrent
// imports than pool workers are in flight; it is generous because the wait is
// legitimate, not a failure.
const pdfiumInstanceTimeout = 5 * time.Minute

// pdfium renders PDF pages by running Chrome's PDF engine compiled to
// WebAssembly under wazero. It is the only path that reproduces these pages:
// Internet Archive comic scans store each page as a JPEG 2000 colour layer plus
// a JBIG2 bilevel ink mask (MRC), so no single embedded image is the page and
// pulling embedded images out yields a textless blur or a colourless line
// drawing. Rasterising composites the layers, and pdfium decodes JPEG 2000 and
// JBIG2 that the Go image stack cannot. The wazero build keeps CGO_ENABLED=0 and
// the static/distroless image, matching how the image codecs are already run.
var (
	pdfiumOnce sync.Once
	pdfiumPool pdfium.Pool
	pdfiumErr  error
)

// pdfiumWorkers is the pool's instance cap. Each instance is a full pdfium wasm
// module, so this is kept small; it comfortably covers the import queue's
// default worker count, and extra imports wait for a free worker rather than
// each spinning up another module's worth of memory.
const pdfiumWorkers = 4

func getPdfiumPool() (pdfium.Pool, error) {
	pdfiumOnce.Do(func() {
		// MinIdle 0 holds no memory when no import is running; MaxIdle 1 keeps one
		// warm module so back-to-back imports skip the wasm compile.
		pdfiumPool, pdfiumErr = webassembly.Init(webassembly.Config{
			MinIdle:  0,
			MaxIdle:  1,
			MaxTotal: pdfiumWorkers,
		})
	})
	return pdfiumPool, pdfiumErr
}

// RasterizePDF renders every page of the PDF to a JPEG under destDir, named so a
// natural sort keeps page order, and returns the page count. The written images
// feed the same pipeline a folder-of-images import runs.
//
// budget caps the total bytes written, the PDF-bomb guard mirroring the upload
// cap. A file that will not open or parse as a PDF is wrapped as ErrNotPDF; a
// PDF whose page count is absurd is ErrTooManyFiles; a render that outgrows the
// budget is ErrPDFTooBig.
func RasterizePDF(ctx context.Context, pdfPath, destDir string, budget int64, progress ProgressFunc) (int, error) {
	if progress == nil {
		progress = func(api.ImportStage, int, int) {}
	}

	pool, err := getPdfiumPool()
	if err != nil {
		return 0, fmt.Errorf("imports: pdfium init: %w", err)
	}
	inst, err := pool.GetInstance(pdfiumInstanceTimeout)
	if err != nil {
		return 0, fmt.Errorf("imports: pdfium instance: %w", err)
	}
	defer inst.Close()

	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return 0, err
	}
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNotPDF, err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNotPDF, err)
	}
	pageCount := pc.PageCount
	if pageCount <= 0 {
		return 0, fmt.Errorf("%w in %s", ErrNoImages, pdfPath)
	}
	if pageCount > maxFiles {
		return 0, fmt.Errorf("%w: %d pages, limit is %d", ErrTooManyFiles, pageCount, maxFiles)
	}
	width := padWidth(pageCount)

	var bytesWritten int64
	for i := 0; i < pageCount; i++ {
		if err := ctx.Err(); err != nil {
			return i, err
		}
		render, err := inst.RenderPageInPixels(&requests.RenderPageInPixels{
			Width:  renderMaxEdge,
			Height: renderMaxEdge,
			Page: requests.Page{
				ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i},
			},
		})
		if err != nil {
			return i, fmt.Errorf("%w: page %d: %v", ErrNotPDF, i+1, err)
		}

		var buf bytes.Buffer
		encErr := jpeg.Encode(&buf, render.Result.Image, &jpeg.Options{Quality: renderQuality})
		render.Cleanup()
		if encErr != nil {
			return i, fmt.Errorf("imports: encode page %d: %w", i+1, encErr)
		}

		bytesWritten += int64(buf.Len())
		if bytesWritten > budget {
			return i, ErrPDFTooBig
		}

		name := fmt.Sprintf("%0*d.jpg", width, i+1)
		if err := os.WriteFile(filepath.Join(destDir, name), buf.Bytes(), 0o644); err != nil {
			return i, err
		}
		progress(api.StageExtracting, i+1, pageCount)
	}

	return pageCount, nil
}
