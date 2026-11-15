package zfs

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// zpoolListArgs is `zpool list` asked for exactly the columns the plugin reads,
// tab-separated without a header (-H) and with exact numbers (-p).
var zpoolListArgs = []string{"list", "-Hp", "-o", "name,health,size,free,capacity,fragmentation"}

// number is one numeric reading: a value, the reason there is none (err),
// or absent — a value zpool does not have for this pool and the plugin
// leaves out rather than reporting it as a fault (fragmentation's '-').
type number struct {
	value  float64
	err    error
	absent bool
}

func value(v float64) number { return number{value: v} }

// listedPool is one line of zpool list.
type listedPool struct {
	name, health                        string
	size, free, capacity, fragmentation number
}

// parseList reads zpool list's output. A line that does not have the six
// columns asked for means the output is not what was asked for, and the
// whole call fails; a column that does not parse is that column's error.
func parseList(out []byte) ([]listedPool, error) {
	var pools []listedPool
	for line := range bytes.Lines(out) {
		s := strings.TrimRight(string(line), "\r\n")
		if s == "" {
			continue
		}
		f := strings.Split(s, "\t")
		if len(f) != 6 || f[0] == "" {
			return nil, fmt.Errorf("unexpected line %q", cutText(s, maxQuote))
		}
		pools = append(pools, listedPool{
			name:          f[0],
			health:        f[1],
			size:          listNumber(f[2], "size", false),
			free:          listNumber(f[3], "free space", false),
			capacity:      listNumber(f[4], "capacity", true),
			fragmentation: listFragmentation(f[5]),
		})
	}
	return pools, nil
}

// errNoValue is a property zpool list prints as '-': it has none for this
// pool (an unavailable pool has no size, free space or capacity).
var errNoValue = errors.New("zpool list prints no")

// listNumber reads one exact, non-negative number; a percentage may carry
// the '%' older releases print even with -p.
func listNumber(s, what string, percent bool) number {
	if s == "-" {
		return number{err: fmt.Errorf("%w %s (the pool is unavailable)", errNoValue, what)}
	}
	t := s
	if percent {
		t = strings.TrimSuffix(t, "%")
	}
	v, err := strconv.ParseFloat(t, 64)
	if err != nil || v < 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return number{err: fmt.Errorf("%s %q is not a number", what, cutText(s, maxQuote))}
	}
	return value(v)
}

// listFragmentation reads the fragmentation column: '-' (a pool without
// the space map histogram feature, or an unavailable one) leaves it out.
func listFragmentation(s string) number {
	if s == "-" {
		return number{absent: true}
	}
	return listNumber(s, "fragmentation", true)
}
