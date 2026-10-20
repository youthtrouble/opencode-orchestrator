package procs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// defaultSearchPath is libuv's _PATH_DEFPATH, searched when the child's environment has no
// PATH at all. Node does not fall back to the parent's PATH (probed on Node 26.5.1, macOS).
const defaultSearchPath = "/usr/bin:/bin"

// accessExecute is X_OK from <unistd.h>; package syscall does not export it.
const accessExecute = 0x1

// StartError is a failure to start a child. Error() is Node's message, "spawn <name> <CODE>",
// for the codes Node delivers as an "error" event (ENOENT and EACCES); any other failure keeps
// Go's text after "spawn <name>: ".
type StartError struct {
	Name string
	Code string // "ENOENT", "EACCES", or "" when Node has no matching error event
	Err  error
}

func (e *StartError) Error() string {
	if e.Code == "" {
		return "spawn " + e.Name + ": " + e.Err.Error()
	}
	return "spawn " + e.Name + " " + e.Code
}

func (e *StartError) Unwrap() error { return e.Err }

// nodeEventCodes are the errnos Node 26 reports through the child's "error" event with the
// "spawn <name> <CODE>" message. Others (ENOTDIR, ELOOP, ENAMETOOLONG, ENOEXEC) make spawn
// throw "spawn <CODE>" synchronously instead, which the bridge does not mirror (probed).
var nodeEventCodes = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT",
	syscall.EACCES: "EACCES",
}

func startError(name string, err error) *StartError {
	failure := &StartError{Name: name, Err: err}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		failure.Code = nodeEventCodes[errno]
	}
	return failure
}

// resolve finds the file execvp would run for name, as an absolute path. A name containing
// "/" is used as is, relative to dir; failures to execute it surface from exec as errnos.
// Otherwise each PATH entry is tried in order as <entry>/<name> (an empty entry is dir). Paths
// are never cleaned lexically: in "link/../bin" the kernel resolves ".." after following the
// symlink, so cleaning it to "bin" would check and run a different file. A directory or a file
// that may not be executed records EACCES and the search goes on; when nothing runs, the error
// is EACCES if one was recorded, else ENOENT.
func resolve(name, dir string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		path, err := absoluteIn(name, dir)
		if err != nil {
			return "", startError(name, err)
		}
		return path, nil
	}
	searchPath, ok := lookupEnv(env, "PATH")
	if !ok {
		searchPath = defaultSearchPath
	}
	failure := syscall.ENOENT
	for _, entry := range strings.Split(searchPath, ":") {
		candidate, err := absoluteIn(candidateIn(entry, name), dir)
		if err != nil {
			return "", startError(name, err)
		}
		err = checkExecutable(candidate)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, fs.ErrPermission) {
			failure = syscall.EACCES
		}
	}
	return "", startError(name, failure)
}

// candidateIn is the file execvp tries for name in one PATH entry; an empty entry is the
// child's directory.
func candidateIn(entry, name string) string {
	if entry == "" {
		return name
	}
	return entry + "/" + name
}

// absoluteIn makes path absolute against dir, the directory the child will run in, so the
// same file is checked here and executed after the child's chdir. A relative dir, or none,
// is taken from the physical working directory as libuv's uv_cwd reports it (syscall.Getwd);
// os.Getwd would prefer $PWD, which may name it through a symlink.
func absoluteIn(path, dir string) (string, error) {
	if !filepath.IsAbs(path) && dir != "" {
		path = dir + "/" + path
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	cwd, err := syscall.Getwd()
	if err != nil {
		return "", err
	}
	return cwd + "/" + path, nil
}

// checkExecutable is the test execve applies: a directory, or a file without execute
// permission for this process, is EACCES.
func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return syscall.EACCES
	}
	return syscall.Access(path, accessExecute)
}

// lookupEnv returns the value of key in env. The last entry wins, as it does for the child:
// exec.Cmd keeps the last of duplicate keys.
func lookupEnv(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if value, found := strings.CutPrefix(env[i], key+"="); found {
			return value, true
		}
	}
	return "", false
}
