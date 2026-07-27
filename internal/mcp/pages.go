package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SeriousBug/dowitcher/internal/cbz"
	"github.com/SeriousBug/dowitcher/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The per-call page caps and widths are what keep one tool call from being a
// denial of service on the agent or on the server.
//
// A full-size comic page is 2-6MB of JPEG, and base64 inflates it by a third, so
// an uncapped image call over a 200-page trade would put hundreds of megabytes
// through a single JSON-RPC response — past every model's context window and
// past what the transport will hold in memory. Ten pages at 1600px is a scene,
// which is the unit an agent actually reasons about; more than that is a request
// to re-read the whole book and should be several calls.
//
// Text is capped harder because OCR is the expensive direction: recognition runs
// under WASM on one core and internal/ocr serialises it deliberately, so five
// pages is already tens of seconds during which no other agent gets any OCR at
// all. 2000px is wider than the image cap because Tesseract reads small
// lettering off resolution, and nothing here is going into a context window.
const (
	maxImagePages = 10
	maxTextPages  = 5
	imageWidth    = 1600
	ocrWidth      = 2000
	// ocrPerPage bounds one page's recognition. A dense splash page is seconds; a
	// page that has not finished in 45s is pathological, and internal/ocr holds
	// the OCR lock the whole time it runs.
	ocrPerPage = 45 * time.Second
)

// ReadComicPagesInput selects pages 1-based, matching what a reader sees printed
// on the page and what get_comic reports as pageCount. The archive is 0-based
// internally, which is an implementation detail an agent should never have to
// know. format has no default: an agent that wants pictures and an agent that
// wants lettering want very different things, and guessing for it would silently
// spend OCR time or silently spend context.
type ReadComicPagesInput struct {
	ComicID  string `json:"comicId" jsonschema:"id of the comic to read"`
	Format   string `json:"format" jsonschema:"'image' for the page pictures themselves, or 'text' for the lettering read off them by OCR"`
	Pages    []int  `json:"pages,omitempty" jsonschema:"individual page numbers to read, 1-based; combines with fromPage/toPage"`
	FromPage int    `json:"fromPage,omitempty" jsonschema:"first page of a range to read, 1-based; must be given together with toPage"`
	ToPage   int    `json:"toPage,omitempty" jsonschema:"last page of the range, 1-based and inclusive; must be given together with fromPage"`
}

const (
	formatImage = "image"
	formatText  = "text"
)

// readComicPages returns the selected pages as MCP content blocks rather than as
// structured output: image pages have to travel as image content for a client to
// render them at all, and returning text the same way keeps one tool with one
// shape of answer instead of two half-used output schemas.
func (s *Server) readComicPages(ctx context.Context, _ *mcp.CallToolRequest, in ReadComicPagesInput) (*mcp.CallToolResult, any, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, nil, errNoUser
	}
	if in.Format != formatImage && in.Format != formatText {
		return nil, nil, fmt.Errorf("format must be %q or %q, got %q", formatImage, formatText, in.Format)
	}
	if in.Format == formatText && s.ocr == nil {
		return nil, nil, errors.New("this instance cannot read text off pages; ask the operator to check DOWITCHER_TESSDATA")
	}

	// Visibility first and through the store, as everywhere else: a comic the
	// caller cannot see must not even reveal how many pages it has.
	comic, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, nil, notFoundOr(err, "comic")
	}
	row, err := s.store.ComicRowByID(in.ComicID)
	if err != nil {
		return nil, nil, dbErr(err)
	}
	a, err := cbz.Open(s.comicFile(row))
	if err != nil {
		log.Printf("mcp read pages, open comic %s (%s): %v", comic.ID, row.Path, err)
		return nil, nil, errors.New("this comic's file could not be read")
	}
	defer a.Close()

	// The archive's own page list is authoritative over the stored count, which
	// can be stale between a rescan and the row it updates.
	want, err := resolvePages(in, a.PageCount())
	if err != nil {
		return nil, nil, err
	}
	if in.Format == formatImage {
		return s.pageImages(a, comic.Title, want)
	}
	return s.pageText(ctx, a, want)
}

func (s *Server) pageImages(a *cbz.Archive, title string, want []int) (*mcp.CallToolResult, any, error) {
	res := &mcp.CallToolResult{}
	for _, i := range want {
		img, err := renderPage(a, i, imageWidth)
		if err != nil {
			return nil, nil, err
		}
		// Each image is preceded by its page number, because content blocks arrive
		// as a flat list and an agent reading four pages has no other way to tell
		// which picture is page 12.
		res.Content = append(res.Content,
			&mcp.TextContent{Text: fmt.Sprintf("Page %d of %q:", i+1, title)},
			&mcp.ImageContent{Data: img, MIMEType: "image/jpeg"},
		)
	}
	return res, nil, nil
}

// pageText recognises the whole selection in one Engine.Text call. Per-page calls
// would stand up and tear down the Tesseract instance for each page, and the
// instance is the expensive part.
func (s *Server) pageText(ctx context.Context, a *cbz.Archive, want []int) (*mcp.CallToolResult, any, error) {
	images := make([][]byte, len(want))
	for n, i := range want {
		img, err := renderPage(a, i, ocrWidth)
		if err != nil {
			return nil, nil, err
		}
		images[n] = img
	}
	texts, err := s.ocr.Text(ctx, images, ocrPerPage)
	if err != nil {
		log.Printf("mcp read pages, ocr: %v", err)
		return nil, nil, errors.New("reading text off these pages failed")
	}
	res := &mcp.CallToolResult{}
	for n, i := range want {
		text := strings.TrimSpace(texts[n])
		if text == "" {
			text = "(no text recognised)"
		}
		res.Content = append(res.Content, &mcp.TextContent{
			Text: fmt.Sprintf("Page %d:\n%s", i+1, text),
		})
	}
	return res, nil, nil
}

// renderPage re-encodes page i through cbz.Thumbnail, which is where the pixel
// caps and the downscale already live. It is not only a size measure: a page may
// be AVIF or WebP, which no MCP client is obliged to render, and Thumbnail hands
// back JPEG whatever went in.
func renderPage(a *cbz.Archive, i, width int) ([]byte, error) {
	rc, _, err := a.Page(i)
	if err != nil {
		return nil, fmt.Errorf("page %d could not be read: %w", i+1, err)
	}
	defer rc.Close()
	img, err := cbz.Thumbnail(rc, width)
	if err != nil {
		log.Printf("mcp read pages, render page %d of %s: %v", i+1, a.Path(), err)
		return nil, fmt.Errorf("page %d could not be decoded", i+1)
	}
	return img, nil
}

// resolvePages turns the 1-based selection into sorted, deduplicated 0-based
// archive indices. The list and the range are unioned rather than exclusive, so
// "the whole fight scene plus the splash page" is one call.
func resolvePages(in ReadComicPagesInput, pageCount int) ([]int, error) {
	if pageCount == 0 {
		return nil, errors.New("this comic has no pages")
	}
	if (in.FromPage == 0) != (in.ToPage == 0) {
		return nil, errors.New("fromPage and toPage must be given together")
	}
	if in.FromPage != 0 && in.ToPage < in.FromPage {
		return nil, fmt.Errorf("toPage (%d) must not be before fromPage (%d)", in.ToPage, in.FromPage)
	}

	seen := map[int]bool{}
	var want []int
	add := func(n int) error {
		if n < 1 || n > pageCount {
			return fmt.Errorf("page %d is out of range: this comic has pages 1 to %d", n, pageCount)
		}
		if seen[n] {
			return nil
		}
		seen[n] = true
		want = append(want, n-1)
		return nil
	}
	for _, n := range in.Pages {
		if err := add(n); err != nil {
			return nil, err
		}
	}
	if in.FromPage != 0 {
		for n := in.FromPage; n <= in.ToPage; n++ {
			if err := add(n); err != nil {
				return nil, err
			}
		}
	}
	if len(want) == 0 {
		return nil, errors.New("select at least one page with pages, or with fromPage and toPage")
	}
	sort.Ints(want)

	limit, format := maxImagePages, formatImage
	if in.Format == formatText {
		limit, format = maxTextPages, formatText
	}
	if len(want) > limit {
		return nil, fmt.Errorf("format %q returns at most %d pages per call, and %d were selected; ask for fewer and call again for the rest",
			format, limit, len(want))
	}
	return want, nil
}

// comicFile turns a stored row into a file to open, the same pairing the HTTP
// layer uses: an upload's path is relative to the uploads dir, a library comic's
// to the library root.
func (s *Server) comicFile(row store.ComicRow) string {
	return filepath.Join(store.ComicFileDir(row, s.uploadsDir, s.libraryRoot), row.Path)
}
