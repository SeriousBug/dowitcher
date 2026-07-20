package imports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// ErrNotPDF means the uploaded file did not open and parse as a PDF.
var ErrNotPDF = errors.New("imports: not a readable pdf")

// ErrPDFTooBig means the images extracted from a PDF exceeded the byte budget:
// a PDF bomb whose embedded images inflate past the upload cap.
var ErrPDFTooBig = errors.New("imports: extracted pdf images exceed the size cap")

// pdfImageExts maps pdfcpu's FileType (the format it decoded an embedded image
// to) onto a file extension the collect step accepts. A filetype missing here is
// one the pipeline cannot read — it is skipped rather than written as junk the
// later stages would silently drop anyway.
var pdfImageExts = map[string]string{
	"jpg":  ".jpg",
	"jpeg": ".jpg",
	"png":  ".png",
	// pdfcpu names its TIFF output "tif"; the pipeline's imageExts only knows the
	// ".tiff" spelling, so normalise to it.
	"tif":  ".tiff",
	"tiff": ".tiff",
}

// ExtractPDF writes every embedded page image under destDir, named so a natural
// sort keeps page order, and returns the count.
//
// Comic PDFs are one embedded full-page scan per page, so this pulls the
// original image bytes out untouched rather than rasterising the page — the
// pure-Go path, and lossless where rasterising would re-render. The written
// images feed the same pipeline a folder-of-images import runs.
//
// budget caps the total bytes written, the same PDF/zip-bomb guard the upload
// path enforces. Reuses ErrNoImages when a PDF carries no extractable images
// (a vector/text PDF, or a file that opened but yielded nothing). A PDF that
// fails to open or parse is wrapped as ErrNotPDF.
func ExtractPDF(ctx context.Context, pdfPath, destDir string, budget int64, progress ProgressFunc) (int, error) {
	if progress == nil {
		progress = func(api.ImportStage, int, int) {}
	}
	// PageCountFile drives the pad width and the progress total. It parses the
	// PDF, so a failure here is the earliest sign the file is unreadable.
	pageCount, err := pdfapi.PageCountFile(pdfPath)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNotPDF, err)
	}
	width := padWidth(pageCount)

	f, err := os.Open(pdfPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	written := 0
	var bytesWritten int64
	receiver := func(img model.Image, single bool, _ int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if img.Reader == nil {
			return nil
		}
		ext, ok := pdfImageExts[img.FileType]
		if !ok {
			// A filetype the pipeline can't read. Skipping keeps a stray mask or
			// an odd colourspace from becoming a page that later stages drop.
			return nil
		}
		// seq disambiguates the rare multi-image page and keeps those images
		// ordered within the page; a single-image page still gets a seq so every
		// name has the same shape and sorts together.
		seq := 0
		if !single {
			seq = img.ObjNr
		}
		name := fmt.Sprintf("%0*d-%02d%s", width, img.PageNr, seq, ext)
		dst := filepath.Join(destDir, name)
		remaining := budget - bytesWritten
		n, err := writePDFImage(dst, img, remaining)
		if err != nil {
			return err
		}
		bytesWritten += n
		written++
		progress(api.StageExtracting, written, pageCount)
		return nil
	}

	// nil selected-pages means every page.
	if err := pdfapi.ExtractImages(f, nil, receiver, model.NewDefaultConfiguration()); err != nil {
		// A budget breach and a cancellation come back through the receiver and
		// mean exactly themselves; only a genuine parse failure is ErrNotPDF.
		if errors.Is(err, ErrPDFTooBig) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return written, err
		}
		return 0, fmt.Errorf("%w: %v", ErrNotPDF, err)
	}
	if written == 0 {
		return 0, fmt.Errorf("%w in %s", ErrNoImages, pdfPath)
	}
	return written, nil
}

// writePDFImage streams one embedded image to disk, refusing to write more than
// budget so a hostile PDF cannot expand past the upload cap.
func writePDFImage(dst string, r io.Reader, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, ErrPDFTooBig
	}
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(r, budget+1))
	if err != nil {
		return n, err
	}
	if n > budget {
		return n, ErrPDFTooBig
	}
	return n, nil
}
