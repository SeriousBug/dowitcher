package cbz

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SeriousBug/longbox/internal/api"
)

// Meta is the metadata we can establish for a comic without a database.
type Meta struct {
	Series  string
	Number  string
	Volume  string
	Title   string
	Summary string
	Tags    []string
}

var (
	// "Series - 05 - Chapter Title" — the dash-delimited form, which is the
	// only pattern that carries a per-issue title, so it is tried first.
	reDashed = regexp.MustCompile(`^(.+?)\s+-\s+(\d+(?:\.\d+)?)\s+-\s+(.+)$`)
	// "Series v02 #05" / "Series v02 05" — an explicit volume marker.
	reVolume = regexp.MustCompile(`(?i)^(.+?)\s+v(\d+)(?:\s+#?(\d+(?:\.\d+)?))?$`)
	// "Series Name 012" / "Series #12" / "Series Ch. 12" — a trailing number.
	reTrailingNum = regexp.MustCompile(`(?i)^(.+?)\s+(?:#|ch(?:apter|\.)?\s*)?(\d+(?:\.\d+)?)$`)
	// Parenthesised and bracketed trailers: "(2021)", "(digital)", "[scan]".
	reTrailers = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]\s*$`)
)

// ParseFilename derives metadata from a CBZ's name. It is the fallback for the
// overwhelming majority of files, which carry no ComicInfo.xml.
//
// It deliberately under-parses: a name with no recognisable series/number
// structure becomes a bare Title. Guessing a series out of "Some Title.cbz"
// would scatter unrelated one-shots into invented series in the library, which
// is worse than leaving the field empty for the user to fill.
func ParseFilename(p string) Meta {
	name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	name = strings.TrimSpace(name)

	// Strip trailers repeatedly: "Series 012 (2021) (digital)" is common.
	base := name
	for {
		stripped := strings.TrimSpace(reTrailers.ReplaceAllString(base, ""))
		if stripped == base || stripped == "" {
			break
		}
		base = stripped
	}
	if base == "" {
		return Meta{Title: name}
	}

	if m := reDashed.FindStringSubmatch(base); m != nil {
		return Meta{
			Series: strings.TrimSpace(m[1]),
			Number: normNumber(m[2]),
			Title:  strings.TrimSpace(m[3]),
		}
	}
	if m := reVolume.FindStringSubmatch(base); m != nil {
		series := strings.TrimSpace(m[1])
		meta := Meta{Series: series, Volume: normNumber(m[2]), Title: series}
		if m[3] != "" {
			meta.Number = normNumber(m[3])
		}
		return meta
	}
	if m := reTrailingNum.FindStringSubmatch(base); m != nil {
		series := strings.TrimSpace(m[1])
		// A series name that is itself only punctuation or empty means the
		// pattern matched noise, not structure.
		if series != "" && strings.ContainsAny(series, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return Meta{Series: series, Number: normNumber(m[2]), Title: series}
		}
	}
	return Meta{Title: base}
}

// normNumber drops the zero padding that only exists to make lexical sorting
// work in file managers. "012" and "12" are the same issue and must not create
// two rows.
func normNumber(s string) string {
	t := strings.TrimLeft(s, "0")
	switch {
	case t == "":
		return "0" // "000" is issue zero, not the empty string.
	case strings.HasPrefix(t, "."):
		return "0" + t // "0.5" keeps its leading zero to stay readable.
	}
	return t
}

// Metadata resolves a comic's metadata, preferring ComicInfo.xml and filling
// each empty field from the filename. The two sources are merged per-field
// rather than one winning wholesale, because a ComicInfo.xml with only a
// Summary is common and should not erase a filename-derived series.
func Metadata(a *Archive) Meta {
	m := ParseFilename(a.path)
	info := a.info
	if info.Series != "" {
		m.Series = info.Series
	}
	if info.Number != "" {
		m.Number = info.Number
	}
	if info.Volume != "" {
		m.Volume = info.Volume
	}
	if info.Title != "" {
		m.Title = info.Title
	}
	if info.Summary != "" {
		m.Summary = info.Summary
	}
	if tags := info.TagList(); len(tags) > 0 {
		m.Tags = tags
	}
	if m.Title == "" {
		m.Title = strings.TrimSuffix(filepath.Base(a.path), filepath.Ext(a.path))
	}
	return m
}

// Comic fills the wire type from an open archive. The store owns ID, Path,
// AddedAt and the sharing fields; everything derivable from the file itself is
// set here so callers cannot disagree about how a CBZ maps onto api.Comic.
func Comic(a *Archive) api.Comic {
	m := Metadata(a)
	c := api.Comic{
		Title:     m.Title,
		Series:    m.Series,
		Number:    m.Number,
		Volume:    m.Volume,
		Summary:   m.Summary,
		PageCount: a.PageCount(),
		Tags:      m.Tags,
	}
	if c.Tags == nil {
		// api.Comic.Tags is not omitempty, and the client indexes it directly.
		c.Tags = []string{}
	}
	return c
}
