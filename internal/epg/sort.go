package epg

import (
	"strings"

	"github.com/jqjiang/tvr/internal/natsort"
)

func naturalLess(a, b string) bool {
	return natsort.Less(a, b)
}

func naturalCompare(a, b string) int {
	return natsort.Compare(a, b)
}

func guideSortKey(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return id
	}
	return name
}
