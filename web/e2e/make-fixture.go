//go:build ignore

// Builds a CBZ for the E2E suite.
//
// The pages are real encoded JPEGs because the server decodes the first one to
// cut a cover, and a comic with no cover never reaches the state this suite is
// about. Generated rather than checked in as a binary blob so the pages stay
// readable as code and cannot rot unnoticed against the CBZ reader.
//
// This mirrors cbzBytes in internal/library/library_test.go, which cannot be
// imported: it is test-only code in a package this does not belong to. The
// `ignore` tag keeps the file out of `go build ./...` while leaving `go run`
// on an explicitly named file working.
//
// Usage: go run make-fixture.go <out.cbz> [pages]
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run make-fixture.go <out.cbz> [pages]")
		os.Exit(2)
	}
	out := os.Args[1]
	pages := 3
	if len(os.Args) > 2 {
		n, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "pages: %v\n", err)
			os.Exit(2)
		}
		pages = n
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range pages {
		w, err := zw.Create(fmt.Sprintf("%02d.jpg", i))
		if err != nil {
			fatal(err)
		}
		// Each page is visibly different, so a test asserting it turned to page
		// two can tell that from page one having rendered twice.
		if _, err := w.Write(pageJPEG(uint8(i))); err != nil {
			fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		fatal(err)
	}
}

func pageJPEG(shade uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 64, 96))
	for y := range 96 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{R: shade * 60, G: uint8(y * 2), B: uint8(x * 4), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 60}); err != nil {
		fatal(err)
	}
	return buf.Bytes()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
