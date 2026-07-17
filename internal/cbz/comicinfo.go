package cbz

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ComicInfo is the subset of the ComicRack ComicInfo.xml schema we use. The
// schema is far wider than this; fields are added when something reads them.
type ComicInfo struct {
	Series    string
	Number    string
	Volume    string
	Title     string
	Summary   string
	PageCount int
	Writer    string
	Genre     string
	Tags      string
	Pages     []ComicInfoPage
}

// ComicInfoPage is one <Page> element. Image is the 0-based index of the image
// within the archive, which is how FrontCover is addressed.
type ComicInfoPage struct {
	Image int
	Type  string
}

// Empty reports whether no ComicInfo.xml was found (or it carried nothing we
// use), which tells callers to fall back to the filename.
func (c ComicInfo) Empty() bool {
	return c.Series == "" && c.Number == "" && c.Volume == "" && c.Title == "" &&
		c.Summary == "" && c.PageCount == 0 && c.Writer == "" && c.Genre == "" &&
		c.Tags == "" && len(c.Pages) == 0
}

// TagList splits the comma-separated Tags field into trimmed, non-empty values.
func (c ComicInfo) TagList() []string {
	var out []string
	for _, t := range strings.Split(c.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// frontCover returns the image index marked Type="FrontCover".
func (c ComicInfo) frontCover() (int, bool) {
	for _, p := range c.Pages {
		if strings.EqualFold(p.Type, "FrontCover") {
			return p.Image, true
		}
	}
	return 0, false
}

// xmlComicInfo mirrors the on-disk element names. It is separate from ComicInfo
// so the exported shape is not tied to the schema's casing and repetition.
type xmlComicInfo struct {
	XMLName   xml.Name `xml:"ComicInfo"`
	Series    string   `xml:"Series"`
	Number    string   `xml:"Number"`
	Volume    string   `xml:"Volume"`
	Title     string   `xml:"Title"`
	Summary   string   `xml:"Summary"`
	PageCount int      `xml:"PageCount"`
	Writer    string   `xml:"Writer"`
	Genre     string   `xml:"Genre"`
	Tags      string   `xml:"Tags"`
	Pages     struct {
		Page []struct {
			Image int    `xml:"Image,attr"`
			Type  string `xml:"Type,attr"`
		} `xml:"Page"`
	} `xml:"Pages"`
}

// readComicInfo finds and parses ComicInfo.xml. Absence is not an error: the
// file is optional metadata and most CBZs in the wild have none. A malformed
// one is not an error either — the comic is still perfectly readable, and
// failing the whole archive over unparseable metadata would hide it from the
// library entirely.
func readComicInfo(files []*zip.File) (ComicInfo, error) {
	f := findComicInfo(files)
	if f == nil {
		return ComicInfo{}, nil
	}
	rc, err := f.Open()
	if err != nil {
		return ComicInfo{}, fmt.Errorf("cbz: open %s: %w", f.Name, err)
	}
	defer rc.Close()
	// Bound the read: ComicInfo.xml is a few KB, and a zip bomb disguised as
	// one must not be able to exhaust memory during a library scan.
	data, err := io.ReadAll(io.LimitReader(rc, 4<<20))
	if err != nil {
		return ComicInfo{}, fmt.Errorf("cbz: read %s: %w", f.Name, err)
	}
	var x xmlComicInfo
	if err := xml.Unmarshal(data, &x); err != nil {
		return ComicInfo{}, nil
	}
	info := ComicInfo{
		Series:    strings.TrimSpace(x.Series),
		Number:    strings.TrimSpace(x.Number),
		Volume:    strings.TrimSpace(x.Volume),
		Title:     strings.TrimSpace(x.Title),
		Summary:   strings.TrimSpace(x.Summary),
		PageCount: x.PageCount,
		Writer:    strings.TrimSpace(x.Writer),
		Genre:     strings.TrimSpace(x.Genre),
		Tags:      strings.TrimSpace(x.Tags),
	}
	for _, p := range x.Pages.Page {
		info.Pages = append(info.Pages, ComicInfoPage{Image: p.Image, Type: p.Type})
	}
	return info, nil
}

// findComicInfo matches the name case-insensitively and at any depth: the
// convention is a root-level "ComicInfo.xml", but writers emit "comicinfo.xml"
// and nest it under the content folder often enough to matter.
func findComicInfo(files []*zip.File) *zip.File {
	var nested *zip.File
	for _, f := range files {
		if f.FileInfo().IsDir() || isJunk(f.Name) || !safeEntryName(f.Name) {
			continue
		}
		if !strings.EqualFold(strings.TrimSuffix(f.Name, "/"), "ComicInfo.xml") {
			if strings.EqualFold(pathBase(f.Name), "ComicInfo.xml") && nested == nil {
				nested = f
			}
			continue
		}
		return f
	}
	return nested
}
