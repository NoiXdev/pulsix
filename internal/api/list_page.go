package api

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/noixdev/pulsix/internal/paging"
)

// PageQuery is the offset-paging half of a paged list's query string
// (backlog 3.97), embedded in each list input. The sort parameter is declared
// per endpoint instead, so the OpenAPI document lists each whitelist. The type
// is exported on purpose: huma skips embedded fields of an unexported type, so
// an embedded pageQuery would silently drop page, per_page and order.
type PageQuery struct {
	Page    int    `query:"page" minimum:"1" maximum:"1000000" doc:"Page number, 1 to 1000000 (higher is 422); when set the list is offset-paged and answers total"`
	PerPage int    `query:"per_page" enum:"10,25,50,100" doc:"Rows per page (default 25); page mode only"`
	Order   string `query:"order" enum:"asc,desc" doc:"Sort direction; page mode only"`
}

// pageOf turns the paging parameters into a paging.Page, or nil when page is
// absent: the list then answers in cursor mode, exactly as before paging
// existed. per_page 0 is the default size. With no sort the endpoint's
// default key applies, descending when defDesc unless order says otherwise; a
// sort without an order is ascending. A cursor and a page together name two
// positions in the list, so they are refused with 422.
func pageOf(q PageQuery, sort, cursor, defSort string, defDesc bool) (*paging.Page, error) {
	if q.Page == 0 {
		return nil, nil
	}
	if cursor != "" {
		return nil, huma.Error422UnprocessableEntity("cursor and page cannot be combined")
	}
	p := &paging.Page{Number: q.Page, Size: q.PerPage, Sort: sort}
	if p.Size == 0 {
		p.Size = paging.DefaultSize
	}
	if p.Sort == "" {
		p.Sort, p.Desc = defSort, defDesc
	}
	switch q.Order {
	case "asc":
		p.Desc = false
	case "desc":
		p.Desc = true
	}
	return p, nil
}
