package procs

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type processTableBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *processTableBuffer) Write(p []byte) (int, error) {
	const limit = 4 << 20
	n := len(p)
	remaining := limit - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// LiveDescendants reads a bounded snapshot from the absolute system ps.
func LiveDescendants(pid int) ([]int, error) {
	var output processTableBuffer
	child, err := Start(Spec{Name: "/bin/ps", Args: []string{"-A", "-o", "pid=", "-o", "ppid=", "-o", "stat="}, Stdout: &output, Grace: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	timer := time.AfterFunc(5*time.Second, func() { child.Signal(syscall.SIGKILL) })
	code, err := child.Wait()
	timer.Stop()
	if err != nil {
		return nil, err
	}
	if code == nil || *code != 0 || output.overflow {
		return nil, fmt.Errorf("process snapshot incomplete")
	}
	entries, err := parseProcessTable(output.String())
	if err != nil {
		return nil, err
	}
	return liveDescendants(pid, entries), nil
}

func parseProcessTable(table string) ([]processEntry, error) {
	var entries []processEntry
	for _, line := range strings.Split(strings.TrimSpace(table), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid process row %q", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, err
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, err
		}
		entries = append(entries, processEntry{pid: pid, parent: parent, zombie: strings.HasPrefix(fields[2], "Z")})
	}
	return entries, nil
}
