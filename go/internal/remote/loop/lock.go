package loop

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// LockHeldError is a serve lock another live process holds. Pid is 0 when the holder has not
// written its pid yet.
type LockHeldError struct{ Pid int }

func (e *LockHeldError) Error() string {
	if e.Pid == 0 {
		return "another orch remote serve is running"
	}
	return fmt.Sprintf("another orch remote serve is running (pid %d)", e.Pid)
}

// Lock is the single-instance lock of orch remote serve, held until Release or exit.
//
// It is an flock(2) on the lock file rather than an exclusive create: the kernel drops the
// lock when its holder dies, so a lock left by a dead serve is taken over with no pid check,
// and two serves starting together cannot both replace a stale file and both run. The file
// holds the holder's pid for the message a second serve prints; it is never removed, since a
// serve that removed it could let a newcomer lock a fresh file while a waiter locks the old.
type Lock struct{ file *os.File }

// AcquireLock takes the lock at path for pid, or returns a *LockHeldError.
func AcquireLock(path string, pid int) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &LockHeldError{Pid: holderPid(file)}
		}
		return nil, &os.PathError{Op: "flock", Path: path, Err: err}
	}
	if err := writePid(file, pid); err != nil {
		file.Close()
		return nil, err
	}
	return &Lock{file: file}, nil
}

// Release gives the lock up.
func (l *Lock) Release() error { return l.file.Close() }

func writePid(file *os.File, pid int) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	_, err := file.WriteAt([]byte(strconv.Itoa(pid)+"\n"), 0)
	return err
}

// holderPid is the pid in the lock file, 0 when it holds none.
func holderPid(file *os.File) int {
	data := make([]byte, 32)
	n, _ := file.ReadAt(data, 0) // a short or failed read leaves no pid to name
	pid, err := strconv.Atoi(strings.TrimSpace(string(data[:n])))
	if err != nil {
		return 0
	}
	return pid
}
