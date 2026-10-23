// Package ownlock coordinates cooperating processes using POSIX record locks.
// All access to a lock inode in its owning process must go through this package:
// closing even an unrelated descriptor for that inode releases its record locks.
// Lock paths must not be renamed or unlinked except by Release. Transactions use
// persistent lock files, separate from the data files their callbacks replace.
package ownlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

// ErrHeld means an owner or a pending transaction already reserves the inode.
var ErrHeld = errors.New("lock held")

var errReservationEnded = errors.New("lock reservation ended")

const maxAttempts = 8

// HeldError includes the kernel's holder PID when available.
type HeldError struct{ PID int }

func (e *HeldError) Error() string { return fmt.Sprintf("lock held (pid %d)", e.PID) }
func (e *HeldError) Unwrap() error { return ErrHeld }

type identity struct{ dev, ino uint64 }

type reservation struct {
	file *os.File
	// A pathname can change to an already reserved inode between lstat and open.
	// Retain that extra descriptor: closing it would release the owner's lock.
	extra []*os.File
	held  bool
	pid   int
}

var registry = struct {
	sync.Mutex
	entries map[identity]*reservation
}{entries: make(map[identity]*reservation)}

var changed = sync.NewCond(&registry.Mutex)

// Test hooks run under the registry mutex and must not call this package.
var afterOpen func(string)
var beforeOpen func(string)
var beforeLock func(string)
var onContention func(string)
var beforeProbeWait func()

// waitForLock runs without the registry mutex. Tests can hold publication after
// the kernel grants the lock, or inject a blocking-syscall failure.
var waitForLock = func(f *os.File) error { return setLock(f, syscall.F_SETLKW) }

var writeOwner = func(f *os.File, pid int) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	_, err := f.WriteAt([]byte(strconv.Itoa(pid)), 0)
	return err
}

// Lock owns a single inode until Release. It must not be copied.
type Lock struct {
	path string
	id   identity
	res  *reservation
	done bool
}

// Acquire takes a non-blocking exclusive lock and writes pid as human-readable
// metadata. Ownership comes from the kernel, never from this text.
func Acquire(path string, pid int) (*Lock, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = absPath
	registry.Lock()
	defer registry.Unlock()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		id, res, existing, err := openReserved(path)
		if err != nil {
			return nil, err
		}
		if existing {
			if res.held {
				return nil, &HeldError{PID: os.Getpid()}
			}
			return nil, heldError(res)
		}
		if beforeLock != nil {
			beforeLock(path)
		}
		if err := setLock(res.file, syscall.F_SETLK); err != nil {
			if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
				if onContention != nil {
					onContention(path)
				}
				err = heldError(res)
			}
			return nil, errors.Join(err, closeReserved(id, res))
		}
		res.held, res.pid = true, os.Getpid()
		if afterOpen != nil {
			afterOpen(path)
		}
		if err := samePath(path, id); err != nil {
			if closeErr := closeReserved(id, res); closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
			continue
		}
		if err := writeOwner(res.file, pid); err != nil {
			return nil, errors.Join(err, closeReserved(id, res))
		}
		return &Lock{path: path, id: id, res: res}, nil
	}
	return nil, fmt.Errorf("lock %q changed inode during %d acquisition attempts", path, maxAttempts)
}

// Release removes the owned path before closing the descriptor. A missing or
// replaced path is not removed; replacement returns an error. The path is anchored
// at Acquire, so changing the working directory does not change what is released.
// Repeated calls have no effect.
func (l *Lock) Release() error {
	registry.Lock()
	defer registry.Unlock()
	if l.done {
		return nil
	}
	l.done = true
	var unlinkErr error
	if err := samePath(l.path, l.id); err == nil {
		unlinkErr = os.Remove(l.path)
	} else if !errors.Is(err, os.ErrNotExist) {
		unlinkErr = err
	}
	return errors.Join(unlinkErr, closeReserved(l.id, l.res))
}

// Probe reports kernel ownership without acquiring a lock. An in-process owner
// is answered without opening or closing a descriptor, including inode aliases.
func Probe(path string) (bool, int, error) {
	registry.Lock()
	defer registry.Unlock()
	for {
		held, pid, err := probePath(path)
		if !errors.Is(err, errReservationEnded) {
			return held, pid, err
		}
	}
}

// probePath requires the registry mutex. A reservation ending during a wait
// restarts pathname resolution, rather than querying its now-closed descriptor.
func probePath(path string) (bool, int, error) {
	id, err := pathIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	if res := registry.entries[id]; res != nil {
		return probeReserved(id, res)
	}
	if beforeOpen != nil {
		beforeOpen(path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	id, err = fileIdentity(f)
	if err != nil {
		return false, 0, errors.Join(err, f.Close())
	}
	if res := registry.entries[id]; res != nil {
		res.extra = append(res.extra, f)
		return probeReserved(id, res)
	}
	held, pid, err := probeFile(f)
	return held, pid, errors.Join(err, f.Close())
}

// Transaction serializes a short callback across processes and goroutines. Its
// lock file persists. Callbacks must not recursively acquire the same inode.
func Transaction(path string, fn func() error) error {
	registry.Lock()
	for attempt := 0; attempt < maxAttempts; {
		id, res, existing, err := openReserved(path)
		if err != nil {
			registry.Unlock()
			return err
		}
		if existing {
			if onContention != nil {
				onContention(path)
			}
			changed.Wait()
			continue
		}
		// Reserve the inode before dropping the mutex to wait in the kernel.
		// Probe and Acquire must not open-and-close it while this wait is pending.
		err = setLock(res.file, syscall.F_SETLK)
		if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EAGAIN) {
			if onContention != nil {
				onContention(path)
			}
			registry.Unlock()
			err = waitForLock(res.file)
			registry.Lock()
		}
		if err != nil {
			err = errors.Join(err, closeReserved(id, res))
			registry.Unlock()
			return err
		}
		res.held, res.pid = true, os.Getpid()
		changed.Broadcast()
		if afterOpen != nil {
			afterOpen(path)
		}
		if err := samePath(path, id); err != nil {
			if closeErr := closeReserved(id, res); closeErr != nil {
				registry.Unlock()
				return errors.Join(err, closeErr)
			}
			attempt++
			continue
		}
		registry.Unlock()
		return runTransaction(id, res, fn)
	}
	registry.Unlock()
	return fmt.Errorf("transaction lock %q changed inode during %d attempts", path, maxAttempts)
}

func runTransaction(id identity, res *reservation, fn func() error) (err error) {
	defer func() {
		registry.Lock()
		err = errors.Join(err, closeReserved(id, res))
		registry.Unlock()
	}()
	return fn()
}

// openReserved requires registry.Lock. It checks aliases before opening, then
// checks again by descriptor to handle a concurrent pathname replacement.
func openReserved(path string) (identity, *reservation, bool, error) {
	id, err := pathIdentity(path)
	if err == nil {
		if res := registry.entries[id]; res != nil {
			return id, res, true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return id, nil, false, err
	}
	if beforeOpen != nil {
		beforeOpen(path)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return id, nil, false, err
	}
	id, err = fileIdentity(f)
	if err != nil {
		return id, nil, false, errors.Join(err, f.Close())
	}
	if res := registry.entries[id]; res != nil {
		res.extra = append(res.extra, f)
		return id, res, true, nil
	}
	res := &reservation{file: f}
	registry.entries[id] = res
	return id, res, false, nil
}

func closeReserved(id identity, res *reservation) error {
	delete(registry.entries, id)
	var err error
	for _, f := range res.extra {
		err = errors.Join(err, f.Close())
	}
	err = errors.Join(err, res.file.Close())
	changed.Broadcast()
	return err
}

func heldError(res *reservation) error {
	_, pid, err := probeFile(res.file)
	if err != nil {
		return err
	}
	return &HeldError{PID: pid}
}

func probeReserved(id identity, res *reservation) (bool, int, error) {
	for !res.held {
		if registry.entries[id] != res {
			return false, 0, errReservationEnded
		}
		held, pid, err := probeFile(res.file)
		if err != nil || held {
			return held, pid, err
		}
		// F_GETLK ignores this process's locks. A pending F_SETLKW may
		// have succeeded before its goroutine could publish held=true.
		// Wait for that publication rather than report a false vacancy.
		if beforeProbeWait != nil {
			beforeProbeWait()
		}
		changed.Wait()
	}
	return true, res.pid, nil
}

func probeFile(f *os.File) (bool, int, error) {
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0}
	err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lock)
	return lock.Type != syscall.F_UNLCK, int(lock.Pid), err
}

func setLock(f *os.File, command int) error {
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0}
	for {
		err := syscall.FcntlFlock(f.Fd(), command, &lock)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func pathIdentity(path string) (identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return identity{}, err
	}
	return statIdentity(info)
}

func fileIdentity(f *os.File) (identity, error) {
	info, err := f.Stat()
	if err != nil {
		return identity{}, err
	}
	return statIdentity(info)
}

func statIdentity(info os.FileInfo) (identity, error) {
	if !info.Mode().IsRegular() {
		return identity{}, fmt.Errorf("lock %q is not a regular file", info.Name())
	}
	stat := info.Sys().(*syscall.Stat_t)
	return identity{uint64(stat.Dev), uint64(stat.Ino)}, nil
}

func samePath(path string, want identity) error {
	got, err := pathIdentity(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("lock %q was replaced", path)
	}
	return nil
}
