package nodefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestNodeFileErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		errno          syscall.Errno
		op, path, want string
	}{
		{syscall.EISDIR, "read", "/box/messages.jsonl", "EISDIR: illegal operation on a directory, read"},
		{syscall.ENOTDIR, "mkdir", "/box/mailbox", "ENOTDIR: not a directory, mkdir '/box/mailbox'"},
		{syscall.EACCES, "open", "/box/messages.jsonl", "EACCES: permission denied, open '/box/messages.jsonl'"},
		{syscall.EPERM, "write", "", "EPERM: operation not permitted, write"},
		{syscall.ENOSPC, "write", "", "ENOSPC: no space left on device, write"},
		{syscall.EBADF, "close", "", "EBADF: bad file descriptor, close"},
		{syscall.ENOENT, "open", "/box/messages.jsonl", "ENOENT: no such file or directory, open '/box/messages.jsonl'"},
		{syscall.EEXIST, "mkdir", "/box/mailbox", "EEXIST: file already exists, mkdir '/box/mailbox'"},
		{syscall.EROFS, "open", "/box/messages.jsonl", "EROFS: read-only file system, open '/box/messages.jsonl'"},
		{syscall.EMFILE, "open", "/box/messages.jsonl", "EMFILE: too many open files, open '/box/messages.jsonl'"},
		{syscall.ELOOP, "open", "/box/messages.jsonl", "ELOOP: too many symbolic links encountered, open '/box/messages.jsonl'"},
		{syscall.ENAMETOOLONG, "mkdir", "/box/mailbox", "ENAMETOOLONG: name too long, mkdir '/box/mailbox'"},
		{syscall.ENFILE, "open", "/box/messages.jsonl", "ENFILE: file table overflow, open '/box/messages.jsonl'"},
		{syscall.EIO, "read", "/box/messages.jsonl", "EIO: i/o error, read"},
		{syscall.EFBIG, "write", "", "EFBIG: file too large, write"},
		{syscall.EINTR, "close", "", "EINTR: interrupted system call, close"},
		{syscall.ENOMEM, "open", "/box/messages.jsonl", "ENOMEM: not enough memory, open '/box/messages.jsonl'"},
		{syscall.EINVAL, "write", "", "EINVAL: invalid argument, write"},
		{syscall.EBUSY, "mkdir", "/box/mailbox", "EBUSY: resource busy or locked, mkdir '/box/mailbox'"},
		{syscall.ETXTBSY, "open", "/box/messages.jsonl", "ETXTBSY: text file is busy, open '/box/messages.jsonl'"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			err := &os.PathError{Op: tc.op, Path: tc.path, Err: tc.errno}
			if got := FileError(err, tc.op, tc.path).Error(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The errnos Node's recursive mkdir reports, which os.MkdirAll does not (it reports ENOTDIR for a
// file and EEXIST for every symlink that does not resolve to a directory).
func TestMkdirRecursiveMatchesNodeErrno(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, root string)
		want syscall.Errno // 0: the directory is created
	}{
		{"fresh tree", func(*testing.T, string) {}, 0},
		{"file at target", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a/b")) }, syscall.EEXIST},
		{"file as parent", func(t *testing.T, root string) { writeFile(t, filepath.Join(root, "a")) }, syscall.ENOTDIR},
		{"symlink to file", func(t *testing.T, root string) {
			writeFile(t, filepath.Join(root, "f"))
			symlink(t, filepath.Join(root, "f"), filepath.Join(root, "a/b"))
		}, syscall.EEXIST},
		{"dangling symlink target", func(t *testing.T, root string) {
			symlink(t, filepath.Join(root, "missing"), filepath.Join(root, "a/b"))
		}, syscall.ENOENT},
		{"dangling symlink parent", func(t *testing.T, root string) {
			symlink(t, filepath.Join(root, "missing"), filepath.Join(root, "a"))
		}, syscall.ENOENT},
		{"symlink loop", func(t *testing.T, root string) {
			symlink(t, filepath.Join(root, "a/b"), filepath.Join(root, "a/b"))
		}, syscall.ELOOP},
		{"symlink to directory", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
				t.Fatal(err)
			}
			symlink(t, filepath.Join(root, "d"), filepath.Join(root, "a/b"))
		}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.seed(t, root)
			dir := filepath.Join(root, "a/b")
			err := mkdirRecursive(dir)
			if tc.want == 0 {
				if err != nil {
					t.Fatalf("got %v, want success", err)
				}
				if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
					t.Fatalf("%s is not a directory after mkdir: %v", dir, statErr)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// process.cwd() is the physical directory even when $PWD names it through a symlink.
func TestResolveUsesThePhysicalWorkingDirectory(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	physical := filepath.Join(root, "physical")
	if err := os.Mkdir(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	symlink(t, physical, link)
	t.Chdir(link)
	t.Setenv("PWD", link)
	for _, tc := range []struct{ in, want string }{
		{"", physical},
		{"a/../b/", filepath.Join(physical, "b")},
		{"/x//y/", "/x/y"},
	} {
		if got, err := Resolve(tc.in); err != nil || got != tc.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestCodeIsNodesErrCode(t *testing.T) {
	dir := t.TempDir()
	_, readErr := ReadFile(dir)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"raw errno", syscall.ENOTDIR, "ENOTDIR"},
		{"path error", &os.PathError{Op: "rename", Path: "/x", Err: syscall.EISDIR}, "EISDIR"},
		{"FileError result", readErr, "EISDIR"},
		{"FileError mkdir", FileError(syscall.EACCES, "mkdir", "/x"), "EACCES"},
		{"kill", syscall.ESRCH, "ESRCH"},
		{"errno outside the table", syscall.EXDEV, ""},
		{"no errno", errors.New("boom"), ""},
	} {
		if got := Code(tc.err); got != tc.want {
			t.Errorf("%s: Code = %q, want %q", tc.name, got, tc.want)
		}
	}
	if readErr.Error() != "EISDIR: illegal operation on a directory, read" {
		t.Errorf("FileError message changed: %q", readErr)
	}
}
