// Package jssort is Array.prototype.sort as V8 implements it (third_party/v8/builtins/
// array-sort.tq): TimSort with binary-insertion runs of at least minrun elements, a stack of
// pending runs merged under the TimSort invariants, and adaptive galloping. With a consistent
// comparator any correct stable sort gives the same order; this port exists for the JS
// comparators that are not consistent (one that never returns 0, say), whose result depends on
// every comparison V8 makes. It therefore makes the same comparisons, in the same order.
package jssort

// minGallopWins is kMinGallopWins: how many consecutive wins switch a merge to galloping.
const minGallopWins = 7

// maxShortLength is the longest array V8 sorts by binary insertion alone, without looking for
// a run (observed with Node v26.5.1: 7 elements are inserted one by one, 8 start with a run).
const maxShortLength = 7

// Sort sorts items in place as items.sort(compare) does in V8. compare returns a negative
// number when a sorts before b, a positive one when after, and 0 when they are equal.
func Sort[T any](items []T, compare func(a, b T) int) {
	if len(items) < 2 {
		return
	}
	s := &sortState[T]{work: items, compare: compare, minGallop: minGallopWins}
	if len(items) <= maxShortLength {
		s.binaryInsertionSort(0, 0, len(items))
		return
	}
	minRun := minRunLength(len(items))
	for low, remaining := 0, len(items); remaining != 0; {
		run := s.countAndMakeRun(low, low+remaining)
		if run < minRun {
			forced := min(minRun, remaining)
			s.binaryInsertionSort(low, low+run, low+forced)
			run = forced
		}
		s.runs = append(s.runs, pendingRun{low, run})
		s.mergeCollapse()
		low += run
		remaining -= run
	}
	s.mergeForceCollapse()
}

type pendingRun struct{ base, length int }

type sortState[T any] struct {
	work      []T
	compare   func(a, b T) int
	minGallop int
	runs      []pendingRun
}

// minRunLength is ComputeMinRunLength: n itself below 64, else a value in [32, 64] such that
// n / minrun is a power of two or slightly less.
func minRunLength(n int) int {
	r := 0
	for n >= 64 {
		r |= n & 1
		n >>= 1
	}
	return n + r
}

// countAndMakeRun returns the length of the run starting at low (high bounds it): the longest
// non-descending prefix, or the longest strictly descending one, which it reverses in place.
func (s *sortState[T]) countAndMakeRun(low, high int) int {
	if low+1 == high {
		return 1
	}
	run := 2
	descending := s.compare(s.work[low+1], s.work[low]) < 0
	previous := s.work[low+1]
	for i := low + 2; i < high; i++ {
		order := s.compare(s.work[i], previous)
		if descending && order >= 0 || !descending && order < 0 {
			break
		}
		previous = s.work[i]
		run++
	}
	if descending {
		for i, j := low, low+run-1; i < j; i, j = i+1, j-1 {
			s.work[i], s.work[j] = s.work[j], s.work[i]
		}
	}
	return run
}

// binaryInsertionSort extends the sorted run work[low:start] to work[low:high]; an empty run
// counts as its first element.
func (s *sortState[T]) binaryInsertionSort(low, start, high int) {
	if start == low {
		start++
	}
	for ; start < high; start++ {
		pivot := s.work[start]
		left, right := low, start
		for left < right {
			mid := left + (right-left)>>1
			if s.compare(pivot, s.work[mid]) < 0 {
				right = mid
			} else {
				left = mid + 1
			}
		}
		copy(s.work[left+1:start+1], s.work[left:start])
		s.work[left] = pivot
	}
}

// invariantHolds reports run_length(n-2) > run_length(n-1) + run_length(n).
func (s *sortState[T]) invariantHolds(n int) bool {
	if n < 2 {
		return true
	}
	return s.runs[n-2].length > s.runs[n-1].length+s.runs[n].length
}

// mergeCollapse merges adjacent pending runs until the stack invariants hold again.
func (s *sortState[T]) mergeCollapse() {
	for len(s.runs) > 1 {
		n := len(s.runs) - 2
		switch {
		case !s.invariantHolds(n+1) || !s.invariantHolds(n):
			if s.runs[n-1].length < s.runs[n+1].length {
				n--
			}
			s.mergeAt(n)
		case s.runs[n].length <= s.runs[n+1].length:
			s.mergeAt(n)
		default:
			return
		}
	}
}

// mergeForceCollapse merges every pending run into one.
func (s *sortState[T]) mergeForceCollapse() {
	for len(s.runs) > 1 {
		n := len(s.runs) - 2
		if n > 0 && s.runs[n-1].length < s.runs[n+1].length {
			n--
		}
		s.mergeAt(n)
	}
}

// mergeAt merges pending runs i and i+1.
func (s *sortState[T]) mergeAt(i int) {
	baseA, lengthA := s.runs[i].base, s.runs[i].length
	baseB, lengthB := s.runs[i+1].base, s.runs[i+1].length
	s.runs[i].length = lengthA + lengthB
	s.runs = append(s.runs[:i+1], s.runs[i+2:]...)

	// Elements of A before where B's first element goes are already in place.
	k := gallopRight(s, s.work, s.work[baseB], baseA, lengthA, 0)
	baseA += k
	lengthA -= k
	if lengthA == 0 {
		return
	}
	// Elements of B after where A's last element goes are already in place.
	lengthB = gallopLeft(s, s.work, s.work[baseA+lengthA-1], baseB, lengthB, lengthB-1)
	if lengthB == 0 {
		return
	}
	if lengthA <= lengthB {
		s.mergeLow(baseA, lengthA, baseB, lengthB)
	} else {
		s.mergeHigh(baseA, lengthA, baseB, lengthB)
	}
}

// gallopLeft returns k in [0, length] with array[base+k-1] < key <= array[base+k], searching
// outward from base+hint by exponential steps, then by bisection.
func gallopLeft[T any](s *sortState[T], array []T, key T, base, length, hint int) int {
	lastOffset, offset := 0, 1
	if s.compare(array[base+hint], key) < 0 {
		maxOffset := length - hint
		for offset < maxOffset && s.compare(array[base+hint+offset], key) < 0 {
			lastOffset, offset = offset, offset<<1+1
		}
		offset = min(offset, maxOffset)
		lastOffset, offset = lastOffset+hint, offset+hint
	} else {
		maxOffset := hint + 1
		for offset < maxOffset && s.compare(array[base+hint-offset], key) >= 0 {
			lastOffset, offset = offset, offset<<1+1
		}
		offset = min(offset, maxOffset)
		lastOffset, offset = hint-offset, hint-lastOffset
	}
	for lastOffset++; lastOffset < offset; {
		m := lastOffset + (offset-lastOffset)>>1
		if s.compare(array[base+m], key) < 0 {
			lastOffset = m + 1
		} else {
			offset = m
		}
	}
	return offset
}

// gallopRight returns k in [0, length] with array[base+k-1] <= key < array[base+k].
func gallopRight[T any](s *sortState[T], array []T, key T, base, length, hint int) int {
	lastOffset, offset := 0, 1
	if s.compare(key, array[base+hint]) < 0 {
		maxOffset := hint + 1
		for offset < maxOffset && s.compare(key, array[base+hint-offset]) < 0 {
			lastOffset, offset = offset, offset<<1+1
		}
		offset = min(offset, maxOffset)
		lastOffset, offset = hint-offset, hint-lastOffset
	} else {
		maxOffset := length - hint
		for offset < maxOffset && s.compare(key, array[base+hint+offset]) >= 0 {
			lastOffset, offset = offset, offset<<1+1
		}
		offset = min(offset, maxOffset)
		lastOffset, offset = lastOffset+hint, offset+hint
	}
	for lastOffset++; lastOffset < offset; {
		m := lastOffset + (offset-lastOffset)>>1
		if s.compare(key, array[base+m]) < 0 {
			offset = m
		} else {
			lastOffset = m + 1
		}
	}
	return offset
}

// mergeLow merges the adjacent runs A (shorter or equal) and B left to right, with A copied
// aside.
func (s *sortState[T]) mergeLow(baseA, lengthA, baseB, lengthB int) {
	work := s.work
	temp := append([]T(nil), work[baseA:baseA+lengthA]...)
	dest, cursorTemp, cursorB := baseA, 0, baseB

	work[dest] = work[cursorB]
	dest, cursorB = dest+1, cursorB+1
	lengthB--

	copyB := func() { // A has one element left; it ends the merge.
		copy(work[dest:dest+lengthB], work[cursorB:cursorB+lengthB])
		work[dest+lengthB] = temp[cursorTemp]
	}
	succeed := func() { copy(work[dest:dest+lengthA], temp[cursorTemp:cursorTemp+lengthA]) }
	if lengthB == 0 {
		succeed()
		return
	}
	if lengthA == 1 {
		copyB()
		return
	}
	minGallop := s.minGallop
	for {
		winsA, winsB := 0, 0
		// One element at a time until one run keeps winning.
		for {
			if s.compare(work[cursorB], temp[cursorTemp]) < 0 {
				work[dest] = work[cursorB]
				dest, cursorB = dest+1, cursorB+1
				winsB, winsA = winsB+1, 0
				if lengthB--; lengthB == 0 {
					succeed()
					return
				}
				if winsB >= minGallop {
					break
				}
			} else {
				work[dest] = temp[cursorTemp]
				dest, cursorTemp = dest+1, cursorTemp+1
				winsA, winsB = winsA+1, 0
				if lengthA--; lengthA == 1 {
					copyB()
					return
				}
				if winsA >= minGallop {
					break
				}
			}
		}
		// Gallop until neither run wins by minGallopWins or more.
		minGallop++
		for first := true; first || winsA >= minGallopWins || winsB >= minGallopWins; first = false {
			minGallop = max(1, minGallop-1)
			s.minGallop = minGallop

			winsA = gallopRight(s, temp, work[cursorB], cursorTemp, lengthA, 0)
			if winsA > 0 {
				copy(work[dest:dest+winsA], temp[cursorTemp:cursorTemp+winsA])
				dest, cursorTemp, lengthA = dest+winsA, cursorTemp+winsA, lengthA-winsA
				if lengthA == 1 {
					copyB()
					return
				}
				if lengthA == 0 { // only an inconsistent comparator gets here
					succeed()
					return
				}
			}
			work[dest] = work[cursorB]
			dest, cursorB = dest+1, cursorB+1
			if lengthB--; lengthB == 0 {
				succeed()
				return
			}

			winsB = gallopLeft(s, work, temp[cursorTemp], cursorB, lengthB, 0)
			if winsB > 0 {
				copy(work[dest:dest+winsB], work[cursorB:cursorB+winsB])
				dest, cursorB, lengthB = dest+winsB, cursorB+winsB, lengthB-winsB
				if lengthB == 0 {
					succeed()
					return
				}
			}
			work[dest] = temp[cursorTemp]
			dest, cursorTemp = dest+1, cursorTemp+1
			if lengthA--; lengthA == 1 {
				copyB()
				return
			}
		}
		minGallop++ // penalize leaving galloping mode
		s.minGallop = minGallop
	}
}

// mergeHigh merges the adjacent runs A and B (shorter) right to left, with B copied aside.
func (s *sortState[T]) mergeHigh(baseA, lengthA, baseB, lengthB int) {
	work := s.work
	temp := append([]T(nil), work[baseB:baseB+lengthB]...)
	dest, cursorTemp, cursorA := baseB+lengthB-1, lengthB-1, baseA+lengthA-1

	work[dest] = work[cursorA]
	dest, cursorA = dest-1, cursorA-1
	lengthA--

	copyA := func() { // B has one element left; it starts the merge.
		dest -= lengthA
		cursorA -= lengthA
		copy(work[dest+1:dest+1+lengthA], work[cursorA+1:cursorA+1+lengthA])
		work[dest] = temp[cursorTemp]
	}
	succeed := func() {
		if lengthB > 0 {
			copy(work[dest-(lengthB-1):dest+1], temp[:lengthB])
		}
	}
	if lengthA == 0 {
		succeed()
		return
	}
	if lengthB == 1 {
		copyA()
		return
	}
	minGallop := s.minGallop
	for {
		winsA, winsB := 0, 0
		for {
			if s.compare(temp[cursorTemp], work[cursorA]) < 0 {
				work[dest] = work[cursorA]
				dest, cursorA = dest-1, cursorA-1
				winsA, winsB = winsA+1, 0
				if lengthA--; lengthA == 0 {
					succeed()
					return
				}
				if winsA >= minGallop {
					break
				}
			} else {
				work[dest] = temp[cursorTemp]
				dest, cursorTemp = dest-1, cursorTemp-1
				winsB, winsA = winsB+1, 0
				if lengthB--; lengthB == 1 {
					copyA()
					return
				}
				if winsB >= minGallop {
					break
				}
			}
		}
		minGallop++
		for first := true; first || winsA >= minGallopWins || winsB >= minGallopWins; first = false {
			minGallop = max(1, minGallop-1)
			s.minGallop = minGallop

			k := gallopRight(s, work, temp[cursorTemp], baseA, lengthA, lengthA-1)
			winsA = lengthA - k
			if winsA > 0 {
				dest, cursorA = dest-winsA, cursorA-winsA
				copy(work[dest+1:dest+1+winsA], work[cursorA+1:cursorA+1+winsA])
				if lengthA -= winsA; lengthA == 0 {
					succeed()
					return
				}
			}
			work[dest] = temp[cursorTemp]
			dest, cursorTemp = dest-1, cursorTemp-1
			if lengthB--; lengthB == 1 {
				copyA()
				return
			}

			k = gallopLeft(s, temp, work[cursorA], 0, lengthB, lengthB-1)
			winsB = lengthB - k
			if winsB > 0 {
				dest, cursorTemp = dest-winsB, cursorTemp-winsB
				copy(work[dest+1:dest+1+winsB], temp[cursorTemp+1:cursorTemp+1+winsB])
				if lengthB -= winsB; lengthB == 1 {
					copyA()
					return
				}
				if lengthB == 0 { // only an inconsistent comparator gets here
					succeed()
					return
				}
			}
			work[dest] = work[cursorA]
			dest, cursorA = dest-1, cursorA-1
			if lengthA--; lengthA == 0 {
				succeed()
				return
			}
		}
		minGallop++
		s.minGallop = minGallop
	}
}
