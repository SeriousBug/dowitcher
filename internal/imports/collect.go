package imports

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// imageExts is package.py's IMAGE_EXTS verbatim. Membership is tested on the
// lowercased extension, so the set holds only lowercase forms.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".tiff": true, ".avif": true,
}

// srcFile is one collected image. Index is its position in natural-sorted
// order, which is the identity every later stage sorts and reports by.
type srcFile struct {
	abs   string
	rel   string // slash-separated, relative to the import root
	index int
	// size is stat'd once at collection. package.py calls p.stat() inside the
	// keeper sort key, costing a syscall per comparison; caching it here makes
	// keeper selection allocation- and syscall-free.
	size int64
}

// natTok is one component of a natural sort key: either a run of digits parsed
// as a number, or a lowercased run of everything else.
type natTok struct {
	num   int64
	str   string
	isNum bool
}

// naturalKey mirrors package.py's natural_key: re.split(r"(\d+)", text) with
// digit runs turned into ints and everything else lowercased.
//
// Python's re.split with a capturing group always yields a non-digit token at
// even indices and a digit token at odd ones (both possibly empty), so two keys
// never compare an int against a str at the same position. This reproduces that
// alternation exactly, which is what makes the element-wise compare below safe.
func naturalKey(text string) []natTok {
	var toks []natTok
	i := 0
	for i <= len(text) {
		// Scan to the next digit run; the text before it is one non-numeric
		// token, emitted even when empty to preserve the even/odd alternation.
		j := i
		for j < len(text) && !isDigit(text[j]) {
			j++
		}
		toks = append(toks, natTok{str: strings.ToLower(text[i:j])})
		if j >= len(text) {
			break
		}
		k := j
		for k < len(text) && isDigit(text[k]) {
			k++
		}
		// A digit run too long for int64 keeps its numeric slot but falls back
		// to a string compare, rather than silently wrapping to a wrong order.
		n, err := strconv.ParseInt(text[j:k], 10, 64)
		if err != nil {
			toks = append(toks, natTok{str: text[j:k], isNum: false})
		} else {
			toks = append(toks, natTok{num: n, isNum: true})
		}
		i = k
	}
	return toks
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// compareNatural orders two natural keys the way Python orders the equivalent
// lists: element-wise, with the shorter key first when one is a prefix.
func compareNatural(a, b []natTok) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		switch {
		case x.isNum && y.isNum:
			if x.num != y.num {
				return cmpInt64(x.num, y.num)
			}
		case x.isNum != y.isNum:
			// Only reachable when a huge digit run fell back to a string
			// token. Order numbers before strings so the result stays a total
			// order instead of depending on comparison sequence.
			if x.isNum {
				return -1
			}
			return 1
		default:
			if x.str != y.str {
				return strings.Compare(x.str, y.str)
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// collect walks root recursively and returns every image file, naturally
// sorted by its POSIX path relative to root.
//
// Sorting on the relative path rather than the basename is deliberate: it keeps
// a per-chapter directory layout in reading order instead of interleaving every
// directory's "01.jpg" together.
func collect(root string) ([]*srcFile, error) {
	var files []*srcFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Symlinks report as neither dir nor regular; WalkDir does not follow
		// them, and package.py's is_file() resolves them. Stat to match.
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // an unstattable entry is skipped, not fatal
		}
		if !imageExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, &srcFile{
			abs:  path,
			rel:  filepath.ToSlash(rel),
			size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	keys := make(map[string][]natTok, len(files))
	for _, f := range files {
		keys[f.rel] = naturalKey(f.rel)
	}
	slices.SortFunc(files, func(a, b *srcFile) int {
		return compareNatural(keys[a.rel], keys[b.rel])
	})
	for i, f := range files {
		f.index = i
	}
	return files, nil
}
