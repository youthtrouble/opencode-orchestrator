package procs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LiveDescendants walks one process-table snapshot, excluding zombies.
// Errors are unknown evidence, never proof that the child has no live work.
func LiveDescendants(pid int) ([]int, error) {
	dirs, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var entries []processEntry
	for _, dir := range dirs {
		id, err := strconv.Atoi(dir.Name())
		if err != nil || !dir.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", dir.Name(), "stat"))
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			continue
		} // exited during the snapshot
		if err != nil {
			return nil, err
		}
		entry, err := parseProcStat(id, string(data))
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return liveDescendants(pid, entries), nil
}

func parseProcStat(pid int, stat string) (processEntry, error) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return processEntry{}, fmt.Errorf("invalid proc stat for %d", pid)
	}
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 2 || len(fields[0]) != 1 {
		return processEntry{}, fmt.Errorf("invalid proc stat for %d", pid)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return processEntry{}, err
	}
	return processEntry{pid: pid, parent: parent, zombie: fields[0] == "Z"}, nil
}
