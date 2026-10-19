package jssort

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

type item struct{ id, k int }

// comparators mirror the ones in testdata/jssort/gen.mjs; two are inconsistent on purpose.
var comparators = map[string]func(a, b item) int{
	"never-equal": func(a, b item) int {
		if a.k < b.k {
			return -1
		}
		return 1
	},
	"hash":     func(a, b item) int { return (a.id*7919+b.id*104729)%5 - 2 },
	"key-hash": func(a, b item) int { return (a.k*31+b.k*17+a.id%3)%7 - 3 },
	"tie-hash": func(a, b item) int {
		if a.k != b.k {
			return a.k - b.k
		}
		return (a.id*7+b.id*5)%3 - 1
	},
	"consistent": func(a, b item) int { return a.k - b.k },
}

// keys reproduces gen.mjs keys(pattern, n, seed).
func keys(pattern string, n, seed int) []int {
	state := 20261019 + n*7 + seed*1000003
	random := func(m int) int { state = state * 48271 % 2147483647; return state % m }
	run, base, j := 0, 0, 0
	out := make([]int, n)
	for i := range out {
		switch pattern {
		case "random-small":
			out[i] = random(4)
		case "random-wide":
			out[i] = random(1000)
		case "ascending-blocks":
			out[i] = i % 40
		case "descending-blocks":
			out[i] = 40 - i%40
		case "two-runs":
			if i < n>>1 {
				out[i] = i
			} else {
				out[i] = i - n>>1
			}
		case "ragged-runs":
			if random(60) == 0 {
				run = random(100)
			} else {
				run++
			}
			out[i] = run
		case "ragged-ties":
			if random(60) == 0 {
				base, j = random(5), 0
			}
			out[i] = base + j/10
			j++
		case "nearly-sorted":
			out[i] = i
			if i%50 == 49 {
				out[i] = i - 25
			}
		case "sorted-plus-one":
			out[i] = i
			if i == n-1 {
				out[i] = -1
			}
		default:
			panic(pattern)
		}
	}
	return out
}

// TestSortMatchesV8 replays testdata/jssort/orders.json, written by Node: sizes around the
// minrun and merge boundaries up to 6000, patterns that make long, short and ragged natural runs
// (and so every merge-stack rule and galloping), under an inconsistent comparator that never
// returns 0, a pseudo-random one and a consistent one (which also pins stability).
func TestSortMatchesV8(t *testing.T) {
	data, err := os.ReadFile("../../testdata/jssort/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	var reference struct {
		Cases []struct {
			Comparator, Pattern string
			N, Seed             int
			SHA256              string
		}
	}
	if err := json.Unmarshal(data, &reference); err != nil {
		t.Fatal(err)
	}
	if len(reference.Cases) < 1000 {
		t.Fatalf("only %d cases", len(reference.Cases))
	}
	for _, c := range reference.Cases {
		items := make([]item, c.N)
		for id, k := range keys(c.Pattern, c.N, c.Seed) {
			items[id] = item{id, k}
		}
		Sort(items, comparators[c.Comparator])
		ids := make([]string, c.N)
		for i, it := range items {
			ids[i] = strconv.Itoa(it.id)
		}
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		if hex.EncodeToString(sum[:]) != c.SHA256 {
			t.Errorf("%s/%s/n=%d/seed=%d: order differs from Node (first ids %v)", c.Comparator, c.Pattern, c.N, c.Seed, ids[:min(len(ids), 8)])
		}
	}
}

func TestMinRunLength(t *testing.T) {
	for n, want := range map[int]int{1: 1, 63: 63, 64: 32, 65: 33, 100: 50, 128: 32, 129: 33, 1000: 63, 3000: 47} {
		if got := minRunLength(n); got != want {
			t.Errorf("minRunLength(%d) = %d, want %d", n, got, want)
		}
	}
}
