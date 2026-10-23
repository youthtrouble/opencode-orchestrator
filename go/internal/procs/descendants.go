package procs

import (
	"errors"
	"sort"
)

// ErrProbeUnsupported means a platform cannot supply trustworthy process evidence.
var ErrProbeUnsupported = errors.New("descendant probe unsupported")

type processEntry struct {
	pid, parent int
	zombie      bool
}

func liveDescendants(root int, entries []processEntry) []int {
	children := make(map[int][]processEntry)
	for _, entry := range entries {
		children[entry.parent] = append(children[entry.parent], entry)
	}
	seen := map[int]bool{root: true}
	queue := []int{root}
	var live []int
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if seen[child.pid] {
				continue
			}
			seen[child.pid] = true
			queue = append(queue, child.pid)
			if !child.zombie {
				live = append(live, child.pid)
			}
		}
	}
	sort.Ints(live)
	return live
}
