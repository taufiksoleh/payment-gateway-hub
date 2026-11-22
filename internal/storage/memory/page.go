package memory

import (
	"sort"
	"time"
)

// pageNewestFirst sorts items by (createdAt, id) descending and returns at
// most limit items that come strictly after cursor (nil for the first page).
// It mirrors the keyset pagination of the Postgres repositories.
func pageNewestFirst[T any](items []T, key func(T) (time.Time, string), cursor *T, limit int) []T {
	before := func(a, b T) bool {
		at, aid := key(a)
		bt, bid := key(b)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return aid > bid
	}
	sort.Slice(items, func(i, j int) bool { return before(items[i], items[j]) })
	if cursor != nil {
		start := sort.Search(len(items), func(i int) bool { return before(*cursor, items[i]) })
		items = items[start:]
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}
