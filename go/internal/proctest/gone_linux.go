package proctest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// Gone reports whether pid has terminated: no process has it, or it is a zombie. A killed
// orphan stays a zombie until init reaps it, which the init of some containers never does, so
// a zombie counts as terminated. The state is read from /proc, which every Linux has, rather
// than from ps, which slim container images lack.
func Gone(pid int) (bool, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return true, nil // ESRCH: the process went while the file was read
	}
	if err != nil {
		return false, err
	}
	// "pid (comm) S ...": comm may hold spaces and parentheses, so the state follows the last ")".
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return false, fmt.Errorf("unexpected /proc/%d/stat %q", pid, stat)
	}
	state := stat[end+2]
	return state == 'Z' || state == 'X', nil
}
