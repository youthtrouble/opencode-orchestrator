// Package nodefs performs the filesystem operations the JS runtime does with node:fs and
// reports their failures with the messages Node prints, so a command that dies of an uncaught
// fs error prints the same error line in both runtimes.
package nodefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Resolve mirrors path.resolve(path): a relative path is joined to the working directory as
// process.cwd() reports it, which is the physical directory. os.Getwd and filepath.Abs prefer
// $PWD, which may name the same directory through a symlink (/tmp for /private/tmp on macOS),
// and the path would then differ in error messages.
func Resolve(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	cwd, err := syscall.Getwd()
	if err != nil {
		return "", FileError(err, "uv_cwd", "")
	}
	return filepath.Join(cwd, path), nil
}

// Exists mirrors fs.existsSync: any stat failure (missing, a dangling or looping symlink, a
// file used as a directory, no permission) counts as missing. Symlinks are followed.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ReadFile mirrors fs.readFileSync(path): an open failure names the path, a read failure
// (EISDIR for a directory) does not.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		operation := "read"
		var pathError *os.PathError
		if errors.As(err, &pathError) && pathError.Op == "open" {
			operation = "open"
		}
		return nil, FileError(err, operation, path)
	}
	return data, nil
}

// WriteFile mirrors fs.writeFileSync(path, data): create or truncate with mode 0666 less the
// umask (an existing file keeps its mode), then one write.
func WriteFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return FileError(err, "open", path)
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return FileError(writeErr, "write", "")
	}
	if closeErr != nil {
		return FileError(closeErr, "close", "")
	}
	return nil
}

// MkdirAll mirrors fs.mkdirSync(dir, {recursive: true}), failing with Node's errno and message.
func MkdirAll(dir string) error {
	if err := mkdirRecursive(dir); err != nil {
		return FileError(err, "mkdir", dir)
	}
	return nil
}

// mkdirRecursive mirrors Node's fs.mkdirSync(dir, {recursive: true}) (MKDirpSync in
// node_file.cc) so the errno matches Node's: os.MkdirAll stats before creating, so a regular file
// at dir reports ENOTDIR and a dangling or looping symlink reports EEXIST, where Node reports
// EEXIST, ENOENT and ELOOP. Node reports every failure against the requested dir; the caller does.
func mkdirRecursive(dir string) error {
	pending := []string{dir}
	for len(pending) > 0 {
		path := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		var errno syscall.Errno // stays 0 when Mkdir succeeds
		if err := os.Mkdir(path, 0o777); err != nil && !errors.As(err, &errno) {
			return err
		}
		switch errno {
		case 0:
		case syscall.EACCES, syscall.ENOSPC, syscall.ENOTDIR, syscall.EPERM:
			return errno
		case syscall.ENOENT:
			parent := filepath.Dir(path)
			if parent == path {
				return syscall.EEXIST
			}
			pending = append(pending, path, parent)
		default:
			info, statErr := os.Stat(path) // follows symlinks, like uv_fs_stat
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				if errno == syscall.EEXIST && len(pending) > 0 {
					return syscall.ENOTDIR
				}
				return syscall.EEXIST
			}
		}
	}
	return nil
}

// nodeErrnoMessages spells each errno the way Node's uvException does. Errnos outside this
// table keep Go's own error text.
var nodeErrnoMessages = map[syscall.Errno]string{
	syscall.EBADF:        "EBADF: bad file descriptor",
	syscall.EACCES:       "EACCES: permission denied",
	syscall.EPERM:        "EPERM: operation not permitted",
	syscall.EISDIR:       "EISDIR: illegal operation on a directory",
	syscall.ENOTDIR:      "ENOTDIR: not a directory",
	syscall.ENOENT:       "ENOENT: no such file or directory",
	syscall.EEXIST:       "EEXIST: file already exists",
	syscall.ENOSPC:       "ENOSPC: no space left on device",
	syscall.EROFS:        "EROFS: read-only file system",
	syscall.EMFILE:       "EMFILE: too many open files",
	syscall.ELOOP:        "ELOOP: too many symbolic links encountered",
	syscall.ENAMETOOLONG: "ENAMETOOLONG: name too long",
	syscall.ENFILE:       "ENFILE: file table overflow",
	syscall.EIO:          "EIO: i/o error",
	syscall.EFBIG:        "EFBIG: file too large",
	syscall.EINTR:        "EINTR: interrupted system call",
	syscall.ENOMEM:       "ENOMEM: not enough memory",
	syscall.EINVAL:       "EINVAL: invalid argument",
	syscall.EBUSY:        "EBUSY: resource busy or locked",
	syscall.ETXTBSY:      "ETXTBSY: text file is busy",
}

// FileError translates a filesystem failure into the message Node prints for the same
// operation (open, read, write, close, mkdir, scandir, ...), reported against the path Node
// names. Node names no path for read, nor where path is "".
func FileError(err error, operation, path string) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	message, ok := nodeErrnoMessages[errno]
	if !ok {
		return err
	}
	if operation == "read" || path == "" {
		return fmt.Errorf("%s, %s", message, operation)
	}
	return fmt.Errorf("%s, %s '%s'", message, operation, path)
}
