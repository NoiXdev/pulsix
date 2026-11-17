package api

import (
	"errors"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/paging"
)

func TestPageOf(t *testing.T) {
	for _, tc := range []struct {
		name     string
		q        PageQuery
		sort     string
		cursor   string
		defSort  string
		defDesc  bool
		want     *paging.Page
		wantCode int
	}{
		{name: "no page is cursor mode", q: PageQuery{}, defSort: "name"},
		{name: "page only uses defaults", q: PageQuery{Page: 2}, defSort: "name",
			want: &paging.Page{Number: 2, Size: 25, Sort: "name"}},
		{name: "default may be descending", q: PageQuery{Page: 1}, defSort: "created", defDesc: true,
			want: &paging.Page{Number: 1, Size: 25, Sort: "created", Desc: true}},
		{name: "sort without order is ascending", q: PageQuery{Page: 1}, sort: "zone", defSort: "created", defDesc: true,
			want: &paging.Page{Number: 1, Size: 25, Sort: "zone"}},
		{name: "order without sort flips the default key", q: PageQuery{Page: 1, Order: "desc"}, defSort: "name",
			want: &paging.Page{Number: 1, Size: 25, Sort: "name", Desc: true}},
		{name: "ascending order on a descending default", q: PageQuery{Page: 1, Order: "asc"}, defSort: "created", defDesc: true,
			want: &paging.Page{Number: 1, Size: 25, Sort: "created"}},
		{name: "explicit size and sort", q: PageQuery{Page: 3, PerPage: 50, Order: "desc"}, sort: "zone", defSort: "name",
			want: &paging.Page{Number: 3, Size: 50, Sort: "zone", Desc: true}},
		{name: "cursor and page together", q: PageQuery{Page: 1}, cursor: "abc", defSort: "name", wantCode: 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pageOf(tc.q, tc.sort, tc.cursor, tc.defSort, tc.defDesc)
			if tc.wantCode != 0 {
				var se huma.StatusError
				if !errors.As(err, &se) || se.GetStatus() != tc.wantCode {
					t.Fatalf("err = %v, want status %d", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("pageOf = %+v, want %+v", got, tc.want)
			}
		})
	}
}
