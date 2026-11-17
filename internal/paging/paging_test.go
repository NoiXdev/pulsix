package paging_test

import (
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/paging"
)

func TestOffset(t *testing.T) {
	for _, tc := range []struct {
		page paging.Page
		want int
	}{
		{paging.Page{Number: 1, Size: 25}, 0},
		{paging.Page{Number: 3, Size: 25}, 50},
		{paging.Page{Number: 2, Size: 10}, 10},
		{paging.Page{Number: 0, Size: 10}, 0},
	} {
		if got := tc.page.Offset(); got != tc.want {
			t.Errorf("%+v.Offset() = %d, want %d", tc.page, got, tc.want)
		}
	}
}

func TestSizes(t *testing.T) {
	if !slices.Equal(paging.Sizes, []int{10, 25, 50, 100}) || paging.DefaultSize != 25 {
		t.Fatalf("Sizes = %v, DefaultSize = %d", paging.Sizes, paging.DefaultSize)
	}
}
