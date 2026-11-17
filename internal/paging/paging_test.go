package paging_test

import (
	"math"
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
		{paging.Page{Number: paging.MaxPage, Size: 100}, (paging.MaxPage - 1) * 100},
		{paging.Page{Number: 1 << 40, Size: 100}, (paging.MaxPage - 1) * 100},
	} {
		if got := tc.page.Offset(); got != tc.want {
			t.Errorf("%+v.Offset() = %d, want %d", tc.page, got, tc.want)
		}
	}
}

func TestOffsetFitsInt32(t *testing.T) {
	if off := (paging.Page{Number: 1 << 60, Size: 100}).Offset(); off > math.MaxInt32 {
		t.Fatalf("offset %d overflows int32", off)
	}
}

func TestSizes(t *testing.T) {
	if !slices.Equal(paging.Sizes, []int{10, 25, 50, 100}) || paging.DefaultSize != 25 {
		t.Fatalf("Sizes = %v, DefaultSize = %d", paging.Sizes, paging.DefaultSize)
	}
}
