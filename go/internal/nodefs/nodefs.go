// Package nodefs performs the filesystem operations the JS runtime does with node:fs and
// reports their failures with the messages Node prints, so a command that dies of an uncaught
// fs error prints the same error line in both runtimes.
package nodefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
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

// Relative mirrors path.relative(from, to): both are resolved as Resolve does, and equal
// paths give "" where filepath.Rel gives ".".
func Relative(from, to string) (string, error) {
	from, err := Resolve(from)
	if err != nil {
		return "", err
	}
	to, err = Resolve(to)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(from, to)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", nil
	}
	return rel, nil
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

// AppendFile mirrors fs.appendFileSync(path, data): open for appending, creating the file with
// mode 0666 less the umask (a dangling symlink creates its target), then one write.
func AppendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
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

// ReadJSONOrNull is `try { return JSON.parse(fs.readFileSync(path, "utf8")) } catch { return
// null }`: any JSON value the file holds, or nil when it cannot be read (missing, a
// directory) or parsed (a BOM, bad JSON). Malformed UTF-8 is decoded as Node decodes it.
func ReadJSONOrNull(path string) any {
	value, _ := ReadJSON(path)
	return value
}

// ReadJSON is ReadJSONOrNull that tells a file holding null (nil, true) from one that cannot
// be read or parsed (nil, false), for the JS `readJson(f, fallback)` helpers, which return
// the fallback only for the latter.
func ReadJSON(path string) (value any, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	value, err = jsjson.Unmarshal([]byte(jsstr.DecodeUTF8(data)))
	if err != nil {
		return nil, false
	}
	return value, true
}

// ReplaceFile is the write-then-rename of the JS writeJson helpers: writeFileSync(path +
// ".tmp", data), then renameSync onto path, so readers see the old or the new content and
// never a partial file. A failed rename leaves the .tmp file, as in the JS.
func ReplaceFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := WriteFile(tmp, data); err != nil {
		return err
	}
	// rename(2) itself: os.Rename refuses a directory target with EEXIST before trying,
	// where Node reports the EISDIR the system call gives.
	if err := syscall.Rename(tmp, path); err != nil {
		return RenameError(err, tmp, path)
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
	syscall.ESRCH:        "ESRCH: no such process",
}

// FileError translates a filesystem failure into the message Node prints for the same
// operation (open, read, write, close, mkdir, scandir, ...), reported against the path Node
// names. Node names no path for read, nor where path is "". The result still unwraps to the
// errno, so Code reports it.
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
		return &nodeError{fmt.Sprintf("%s, %s", message, operation), errno}
	}
	return &nodeError{fmt.Sprintf("%s, %s '%s'", message, operation, path), errno}
}

// RenameError is FileError for fs.renameSync(from, to), which Node reports against both
// paths: "<message>, rename '<from>' -> '<to>'".
func RenameError(err error, from, to string) error {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err
	}
	message, ok := nodeErrnoMessages[errno]
	if !ok {
		return err
	}
	return &nodeError{fmt.Sprintf("%s, rename '%s' -> '%s'", message, from, to), errno}
}

// nodeError is a failure worded as Node words it, carrying the errno it came from.
type nodeError struct {
	message string
	errno   syscall.Errno
}

func (e *nodeError) Error() string { return e.message }
func (e *nodeError) Unwrap() error { return e.errno }

// Code is the `err.code` Node gives the failure (e.g. "EISDIR"), from the same errno table as
// FileError, or "" when err carries no errno that table knows; the JS then falls back to
// String(err), as in `err.code || String(err)`.
func Code(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	code, _, _ := strings.Cut(nodeErrnoMessages[errno], ":")
	return code
}

// ErrorCode is the JS `err.code || String(err)`: Node's code for a filesystem failure, else
// String(err) for the TypeErrors and RangeErrors jsjson models ("TypeError: <message>"). For
// an errno outside Code's table, and any other error, it approximates String(err) as
// "Error: " and Go's text, where Node would have given the errno's code.
func ErrorCode(err error) string {
	if code := Code(err); code != "" {
		return code
	}
	return jsjson.ErrorString(err)
}
