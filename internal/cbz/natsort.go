package cbz

import (
	"strings"
	"unicode"
)

// natLess orders names the way a human orders pages: digit runs compare as
// integers, so "2.jpg" precedes "10.jpg" where a lexical sort would not. Page
// order in a CBZ is defined by nothing but the entry names, so this comparator
// is the whole of our page ordering.
func natLess(a, b string) bool {
	return natCompare(a, b) < 0
}

func natCompare(a, b string) int {
	ac, bc := natChunks(a), natChunks(b)
	for i := 0; i < len(ac) && i < len(bc); i++ {
		if c := compareChunk(ac[i], bc[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(ac) < len(bc):
		return -1
	case len(ac) > len(bc):
		return 1
	}
	// Chunks compared equal but the raw strings may still differ in case or in
	// digit padding ("01" vs "1"). Fall back to a byte compare so the order is
	// total and stable rather than dependent on sort implementation.
	return strings.Compare(a, b)
}

type chunk struct {
	text    string
	digits  string
	isDigit bool
}

func compareChunk(a, b chunk) int {
	if a.isDigit && b.isDigit {
		// Compare digit strings by value without parsing: leading zeros are
		// stripped, then longer means larger. This avoids overflow on the
		// absurdly long digit runs that show up in hash-named entries.
		x, y := strings.TrimLeft(a.digits, "0"), strings.TrimLeft(b.digits, "0")
		if len(x) != len(y) {
			if len(x) < len(y) {
				return -1
			}
			return 1
		}
		return strings.Compare(x, y)
	}
	if a.isDigit != b.isDigit {
		// Digits sort before letters, matching how "1.jpg" precedes "a.jpg".
		if a.isDigit {
			return -1
		}
		return 1
	}
	return strings.Compare(a.text, b.text)
}

// natChunks splits a name into alternating non-digit and digit runs. Non-digit
// runs are lowercased so case never outranks position.
func natChunks(s string) []chunk {
	var out []chunk
	rs := []rune(s)
	for i := 0; i < len(rs); {
		isDigit := unicode.IsDigit(rs[i])
		j := i
		for j < len(rs) && unicode.IsDigit(rs[j]) == isDigit {
			j++
		}
		run := string(rs[i:j])
		if isDigit {
			out = append(out, chunk{digits: run, isDigit: true})
		} else {
			out = append(out, chunk{text: strings.ToLower(run)})
		}
		i = j
	}
	return out
}
