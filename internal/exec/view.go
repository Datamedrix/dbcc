package exec

// View is a local sort and filter applied on top of a free (non
// table-origin) result by the grid (spec 10.4, 10.5). Export applies it
// while streaming (spec 10.7): Keep filters each row; when Sorted reports
// true, export loads all rows first and writes them in Order.
type View interface {
	Keep(r Row) bool
	Sorted() bool
	Order(rows []Row) []Row
}
