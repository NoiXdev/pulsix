// Package paging holds the offset-paging types shared by every list that
// answers pages with a total (hosts, runs, alerts, users, activity).
package paging

// Sizes are the page sizes a list accepts.
var Sizes = []int{10, 25, 50, 100}

// DefaultSize is the page size used when a request names none.
const DefaultSize = 25

// MaxPage is the highest page number a list accepts. With the largest page
// size the offset stays at 100 million, far inside an int32 (the SQL offset
// parameter) and past any real list; a page between the last and MaxPage
// answers 200 with no rows.
const MaxPage = 1_000_000

// Page is one requested page of an offset-paged list.
type Page struct {
	Number int // 1-based
	Size   int
	Sort   string // the endpoint's sort key, already validated against its whitelist
	Desc   bool
}

// Offset is the number of rows before the page. A page below 1 starts at 0 and
// one above MaxPage counts as MaxPage, so the result always fits an int32.
func (p Page) Offset() int {
	if p.Number < 1 {
		return 0
	}
	return (min(p.Number, MaxPage) - 1) * p.Size
}
