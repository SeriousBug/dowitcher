package cbz

import (
	"sort"
	"testing"
)

func TestNatSort(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "unpadded numbers order by value",
			in:   []string{"10.jpg", "2.jpg", "1.jpg"},
			want: []string{"1.jpg", "2.jpg", "10.jpg"},
		},
		{
			name: "zero padded",
			in:   []string{"010.jpg", "002.jpg", "001.jpg", "100.jpg"},
			want: []string{"001.jpg", "002.jpg", "010.jpg", "100.jpg"},
		},
		{
			name: "mixed padding of the same value",
			in:   []string{"p2.jpg", "p02.jpg", "p10.jpg"},
			want: []string{"p02.jpg", "p2.jpg", "p10.jpg"},
		},
		{
			name: "mixed prefixes",
			in:   []string{"page10.jpg", "cover.jpg", "page2.jpg", "credits.jpg"},
			want: []string{"cover.jpg", "credits.jpg", "page2.jpg", "page10.jpg"},
		},
		{
			name: "nested chapters compare on the first differing run",
			in:   []string{"ch10/p1.jpg", "ch1/p10.jpg", "ch1/p2.jpg", "ch2/p1.jpg"},
			want: []string{"ch1/p2.jpg", "ch1/p10.jpg", "ch2/p1.jpg", "ch10/p1.jpg"},
		},
		{
			name: "no digits falls back to lowercased text",
			in:   []string{"beta.jpg", "Alpha.jpg", "gamma.jpg"},
			want: []string{"Alpha.jpg", "beta.jpg", "gamma.jpg"},
		},
		{
			name: "case does not outrank position",
			in:   []string{"Page10.jpg", "page2.jpg"},
			want: []string{"page2.jpg", "Page10.jpg"},
		},
		{
			name: "digits sort before letters",
			in:   []string{"a.jpg", "1.jpg"},
			want: []string{"1.jpg", "a.jpg"},
		},
		{
			name: "multiple digit runs",
			in:   []string{"v2c10.jpg", "v10c1.jpg", "v2c2.jpg"},
			want: []string{"v2c2.jpg", "v2c10.jpg", "v10c1.jpg"},
		},
		{
			name: "long digit runs do not overflow",
			in:   []string{"99999999999999999999999.jpg", "3.jpg"},
			want: []string{"3.jpg", "99999999999999999999999.jpg"},
		},
		{
			name: "real world flat naming",
			in:   []string{"120.avif", "1.avif", "9.avif", "10.avif"},
			want: []string{"1.avif", "9.avif", "10.avif", "120.avif"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := append([]string(nil), tc.in...)
			sort.Slice(got, func(i, j int) bool { return natLess(got[i], got[j]) })
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestNatCompareIsTotal(t *testing.T) {
	names := []string{"1.jpg", "01.jpg", "a.jpg", "A.jpg", "ch1/p1.jpg", ""}
	for _, a := range names {
		for _, b := range names {
			if natCompare(a, b) != -natCompare(b, a) {
				t.Errorf("asymmetric compare for %q vs %q", a, b)
			}
		}
	}
	if natCompare("1.jpg", "1.jpg") != 0 {
		t.Error("equal names must compare equal")
	}
	// Same chunks but different raw text must still order deterministically.
	if natCompare("01.jpg", "1.jpg") == 0 {
		t.Error("differently padded names must not compare equal")
	}
}
