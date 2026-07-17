package cbz

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"testing"
)

const fullComicInfo = `<?xml version="1.0" encoding="utf-8"?>
<ComicInfo xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <Series>Invincible</Series>
  <Number>7</Number>
  <Volume>2</Volume>
  <Title>Family Matters</Title>
  <Summary>Mark learns something.</Summary>
  <PageCount>2</PageCount>
  <Writer>Robert Kirkman</Writer>
  <Genre>Superhero</Genre>
  <Tags>action, drama , ,capes</Tags>
  <Pages>
    <Page Image="0" Type="FrontCover"/>
    <Page Image="1"/>
  </Pages>
</ComicInfo>`

func TestComicInfoParsed(t *testing.T) {
	p := writeZip(t, "Invincible 007.cbz", []entry{
		{"1.png", pngBytes(t, 4, 4)},
		{"2.png", pngBytes(t, 4, 4)},
		{"ComicInfo.xml", []byte(fullComicInfo)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got := a.Info()
	if got.Series != "Invincible" || got.Number != "7" || got.Volume != "2" ||
		got.Title != "Family Matters" || got.Summary != "Mark learns something." ||
		got.PageCount != 2 || got.Writer != "Robert Kirkman" || got.Genre != "Superhero" {
		t.Fatalf("unexpected ComicInfo: %+v", got)
	}
	if got.Empty() {
		t.Error("Empty() must be false for a populated ComicInfo")
	}
	tags := got.TagList()
	if len(tags) != 3 || tags[0] != "action" || tags[1] != "drama" || tags[2] != "capes" {
		t.Errorf("got tags %v, want [action drama capes]", tags)
	}
	if n, ok := got.frontCover(); !ok || n != 0 {
		t.Errorf("got frontCover %d,%v want 0,true", n, ok)
	}
}

func TestComicInfoAbsentIsNotAnError(t *testing.T) {
	p := writeZip(t, "Plain 01.cbz", []entry{{"1.png", pngBytes(t, 4, 4)}})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Info().Empty() {
		t.Errorf("got %+v, want the zero ComicInfo", a.Info())
	}
}

func TestComicInfoCaseInsensitiveAndNested(t *testing.T) {
	for _, name := range []string{"comicinfo.xml", "COMICINFO.XML", "content/ComicInfo.xml"} {
		t.Run(name, func(t *testing.T) {
			p := writeZip(t, "Case 01.cbz", []entry{
				{"1.png", pngBytes(t, 4, 4)},
				{name, []byte(`<ComicInfo><Series>Found</Series></ComicInfo>`)},
			})
			a, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if a.Info().Series != "Found" {
				t.Errorf("ComicInfo at %q was not picked up: %+v", name, a.Info())
			}
		})
	}
}

func TestComicInfoMalformedIsNotAnError(t *testing.T) {
	p := writeZip(t, "Bad 01.cbz", []entry{
		{"1.png", pngBytes(t, 4, 4)},
		{"ComicInfo.xml", []byte("<ComicInfo><Series>unclosed")},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatalf("malformed metadata must not fail the archive: %v", err)
	}
	defer a.Close()
	if !a.Info().Empty() {
		t.Errorf("got %+v, want the zero ComicInfo", a.Info())
	}
	if a.PageCount() != 1 {
		t.Errorf("got %d pages, want 1", a.PageCount())
	}
}

func TestComicInfoXMLIsNotAPage(t *testing.T) {
	p := writeZip(t, "NotAPage 01.cbz", []entry{
		{"ComicInfo.xml", []byte(fullComicInfo)},
		{"1.png", pngBytes(t, 4, 4)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.PageCount() != 1 {
		t.Fatalf("got %d pages, want 1", a.PageCount())
	}
}

func TestMetadataPrefersComicInfoPerField(t *testing.T) {
	p := writeZip(t, "Fallback Series 003.cbz", []entry{
		{"1.png", pngBytes(t, 4, 4)},
		// Only a summary: the filename must still supply series and number.
		{"ComicInfo.xml", []byte(`<ComicInfo><Summary>From XML</Summary></ComicInfo>`)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m := Metadata(a)
	if m.Series != "Fallback Series" || m.Number != "3" || m.Summary != "From XML" {
		t.Errorf("got %+v, want series=Fallback Series number=3 summary=From XML", m)
	}
}

func TestComicFillsWireType(t *testing.T) {
	p := writeZip(t, "Invincible 007.cbz", []entry{
		{"1.png", pngBytes(t, 4, 4)},
		{"2.png", pngBytes(t, 4, 4)},
		{"ComicInfo.xml", []byte(fullComicInfo)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := Comic(a)
	if c.Series != "Invincible" || c.Number != "7" || c.Volume != "2" ||
		c.Title != "Family Matters" || c.PageCount != 2 {
		t.Errorf("unexpected comic: %+v", c)
	}
	if len(c.Tags) != 3 {
		t.Errorf("got tags %v, want 3", c.Tags)
	}
}

func TestComicTagsNeverNil(t *testing.T) {
	p := writeZip(t, "Plain 01.cbz", []entry{{"1.png", pngBytes(t, 4, 4)}})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if c := Comic(a); c.Tags == nil {
		t.Error("Tags must marshal as [] rather than null")
	}
}

func TestThumbnail(t *testing.T) {
	src := pngBytes(t, 400, 600)
	out, err := Thumbnail(bytes.NewReader(src), 100)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("thumbnail is not a decodable JPEG: %v", err)
	}
	if cfg.Width != 100 || cfg.Height != 150 {
		t.Errorf("got %dx%d, want 100x150", cfg.Width, cfg.Height)
	}
}

func TestThumbnailDoesNotUpscale(t *testing.T) {
	out, err := Thumbnail(bytes.NewReader(pngBytes(t, 50, 20)), 100)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 50 || cfg.Height != 20 {
		t.Errorf("got %dx%d, want 50x20", cfg.Width, cfg.Height)
	}
}

func TestThumbnailExtremeAspectRatioKeepsAtLeastOneRow(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4000, 3)), nil); err != nil {
		t.Fatal(err)
	}
	out, err := Thumbnail(&buf, 10)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 10 || cfg.Height < 1 {
		t.Errorf("got %dx%d, want 10x>=1", cfg.Width, cfg.Height)
	}
}

func TestThumbnailErrors(t *testing.T) {
	if _, err := Thumbnail(bytes.NewReader(pngBytes(t, 10, 10)), 0); err == nil {
		t.Error("expected an error for a non-positive width")
	}
	if _, err := Thumbnail(bytes.NewReader([]byte("not an image")), 100); err == nil {
		t.Error("expected an error for undecodable input")
	}
}

func TestDimensions(t *testing.T) {
	w, h, err := Dimensions(bytes.NewReader(pngBytes(t, 123, 45)))
	if err != nil {
		t.Fatal(err)
	}
	if w != 123 || h != 45 {
		t.Errorf("got %dx%d, want 123x45", w, h)
	}
	if _, _, err := Dimensions(bytes.NewReader([]byte("garbage"))); err == nil {
		t.Error("expected an error for undecodable input")
	}
}

func TestDimensionsWebP(t *testing.T) {
	// A real 14x9 lossy WebP, checking that the x/image/webp decoder is
	// actually registered — nothing else in the test set would catch its
	// absence, since Go has no WebP encoder to generate one with.
	data, err := base64.StdEncoding.DecodeString(
		"UklGRkoAAABXRUJQVlA4ID4AAADwAQCdASoOAAkAAgA0JbACdLoAAwgvv8AA/vxr" +
			"sBMjZB0Ck7nltKCHOzK82m9nihnq6+5WXkf9Dy07GqAAAA==")
	if err != nil {
		t.Fatal(err)
	}
	w, h, err := Dimensions(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if w != 14 || h != 9 {
		t.Errorf("got %dx%d, want 14x9", w, h)
	}
}
