package cbz

import "testing"

func TestParseFilename(t *testing.T) {
	tests := []struct {
		in     string
		series string
		number string
		volume string
		title  string
	}{
		{
			in:     "Series Name 012 (2021).cbz",
			series: "Series Name", number: "12", title: "Series Name",
		},
		{
			in:     "Series Name v02 #05.cbz",
			series: "Series Name", number: "5", volume: "2", title: "Series Name",
		},
		{
			in:     "Series - 05 - Chapter Title.cbz",
			series: "Series", number: "5", title: "Chapter Title",
		},
		{
			// The plain case: no structure, so no series is invented.
			in:    "Some Title.cbz",
			title: "Some Title",
		},
		{
			// Must NOT be over-parsed: "2000" is part of the name, not an
			// issue number, and there is nothing else to make a series from.
			in:    "Akira.cbz",
			title: "Akira",
		},
		{
			in:     "Berserk v01.cbz",
			series: "Berserk", volume: "1", title: "Berserk",
		},
		{
			in:     "Saga #001 (2012) (digital).cbz",
			series: "Saga", number: "1", title: "Saga",
		},
		{
			in:     "One Piece Ch. 1044.cbz",
			series: "One Piece", number: "1044", title: "One Piece",
		},
		{
			in:     "Series 000.cbz",
			series: "Series", number: "0", title: "Series",
		},
		{
			in:     "Series 05.5.cbz",
			series: "Series", number: "5.5", title: "Series",
		},
		{
			in:     "Series - 5 - Title (2021) [scan].cbz",
			series: "Series", number: "5", title: "Title",
		},
		{
			// A bare number with no series text is a title, not issue 5 of
			// nothing.
			in:    "05.cbz",
			title: "05",
		},
		{
			in:    "On Full Display (avif-q70).cbz",
			title: "On Full Display",
		},
		{
			in:    "/library/nested/dir/On the Road.cbz",
			title: "On the Road",
		},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := ParseFilename(tc.in)
			if got.Series != tc.series || got.Number != tc.number ||
				got.Volume != tc.volume || got.Title != tc.title {
				t.Errorf("got series=%q number=%q volume=%q title=%q, want series=%q number=%q volume=%q title=%q",
					got.Series, got.Number, got.Volume, got.Title,
					tc.series, tc.number, tc.volume, tc.title)
			}
		})
	}
}
