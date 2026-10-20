package nodefs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
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
			err := mkdirRecursive(dir, 0o777)
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

func TestRelativeIsPathRelative(t *testing.T) {
	cwd, err := syscall.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ from, to, want string }{
		{"/a/b", "/a/b/c/d.md", "c/d.md"},
		{"/a/b", "/a/c", "../c"},
		{"/a/b", "/a/b", ""},
		{"/a/b/", "/a/b", ""},
		{"/", "/x", "x"},
		{cwd, "rel/x", "rel/x"},
		{"/a/b", "/a/b/./c/../d", "d"},
	} {
		got, err := Relative(tc.from, tc.to)
		if err != nil || got != tc.want {
			t.Errorf("Relative(%q, %q) = %q, %v; want %q", tc.from, tc.to, got, err, tc.want)
		}
	}
}

func TestAppendFileAppendsAndCreatesAsAppendFileSync(t *testing.T) {
	dir := t.TempDir()
	umask := syscall.Umask(0)
	syscall.Umask(umask)

	created := filepath.Join(dir, "created")
	for _, chunk := range []string{"a\n", "b"} {
		if err := AppendFile(created, []byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(created); string(data) != "a\nb" {
		t.Errorf("content %q, want %q", data, "a\nb")
	}
	if info, err := os.Stat(created); err != nil || info.Mode().Perm() != 0o666&^os.FileMode(umask) {
		t.Errorf("mode %v (%v), want 0666 less the umask %03o", info.Mode(), err, umask)
	}

	target := filepath.Join(dir, "target")
	symlink(t, target, filepath.Join(dir, "dangling"))
	if err := AppendFile(filepath.Join(dir, "dangling"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "x" {
		t.Errorf("a dangling symlink's target holds %q (%v), want it created with %q", data, err, "x")
	}
}

func TestAppendFileFailsWithNodesOpenErrors(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, path, want string
	}{
		{"directory", dir, "EISDIR: illegal operation on a directory, open '" + dir + "'"},
		{"missing parent", filepath.Join(dir, "absent", "f"), "ENOENT: no such file or directory, open '" + filepath.Join(dir, "absent", "f") + "'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := AppendFile(tc.path, []byte("x")); err == nil || err.Error() != tc.want {
				t.Errorf("AppendFile = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestReadJSONOrNullReadsAnyValueAndNilOnFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.json")
	for _, tc := range []struct {
		name, content string
		want          any
	}{
		{"number", `5`, 5.0},
		{"invalid UTF-8 decoded as Node does", "\"a\xffb\"", "a\uFFFDb"},
		{"BOM", "\xef\xbb\xbf{}", nil},
		{"unparsable", `{"to":`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := ReadJSONOrNull(file); got != tc.want {
				t.Errorf("ReadJSONOrNull = %#v, want %#v", got, tc.want)
			}
		})
	}
	for name, path := range map[string]string{"missing": filepath.Join(dir, "absent"), "directory": dir} {
		if got := ReadJSONOrNull(path); got != nil {
			t.Errorf("%s: ReadJSONOrNull = %#v, want nil", name, got)
		}
	}
}

// withUmask022 runs the rest of the test under umask 022, so a mode the code asks for is not
// masked by the caller's own umask. Tests using it must not run in parallel: the umask is
// process-wide.
func withUmask022(t *testing.T) {
	t.Helper()
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
}

func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("%s: mode %v, want a regular %v", path, info.Mode(), want)
	}
}

func wantNoTemps(t *testing.T, dir string) {
	t.Helper()
	if temps, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*")); len(temps) > 0 {
		t.Errorf("temps left behind: %v", temps)
	}
}

func TestWritePrivateFileReplacesTheContentOwnerOnly(t *testing.T) {
	withUmask022(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	writeFile(t, target) // 0644, as the JS left it
	if err := WritePrivateFile(target, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new\n" {
		t.Errorf("content = %q", data)
	}
	wantMode(t, target, 0o600)
	wantNoTemps(t, dir)
}

func TestWritePrivateFileReplacesAPlantedSymlinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "state.json")
	symlink(t, victim, target)
	if err := WritePrivateFile(target, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	wantMode(t, target, 0o600)
	if data, _ := os.ReadFile(victim); string(data) != "keep" {
		t.Errorf("symlink target = %q, want it untouched", data)
	}
}

func TestWritePrivateFileKeepsTheTempBesideARelativePath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := WritePrivateFile("state.json", []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "state.json")); string(data) != "new\n" {
		t.Errorf("content = %q", data)
	}
}

// The JS and the old Go wrote the fixed `<file>.tmp`; whatever sits there now (a leftover, or a
// symlink planted to redirect the write) is neither opened nor removed.
func TestWritePrivateFileLeavesTheOldFixedTmpNameAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	if err := os.WriteFile(target+".tmp", []byte("planted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(target, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "new\n" {
		t.Errorf("content = %q", data)
	}
	if data, _ := os.ReadFile(target + ".tmp"); string(data) != "planted" {
		t.Errorf(".tmp = %q, want it untouched", data)
	}
}

// Two writers (a worker publishing while the supervisor redispatches its lane) each use their
// own temp, so a reader only ever sees one writer's complete document.
func TestWritePrivateFileConcurrentWritersNeverMixContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "lane.json")
	if err := WritePrivateFile(target, []byte(`{"writer":"seed"}`)); err != nil {
		t.Fatal(err)
	}
	const writes = 200
	var writers sync.WaitGroup
	errs := make(chan error, 2*writes)
	for _, writer := range []string{"a", "b"} {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := range writes {
				doc := fmt.Sprintf(`{"writer":%q,"n":%d,"pad":%q}`, writer, i, strings.Repeat(writer, 4096))
				if err := WritePrivateFile(target, []byte(doc)); err != nil {
					errs <- err
				}
			}
		}()
	}
	stop := make(chan struct{})
	reads := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				reads <- nil
				return
			default:
			}
			data, err := os.ReadFile(target)
			if err == nil && !json.Valid(data) {
				err = fmt.Errorf("read a mixed or partial document: %.80q", data)
			}
			if err != nil {
				reads <- err
				return
			}
		}
	}()
	writers.Wait()
	close(stop)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err := <-reads; err != nil {
		t.Error(err)
	}
	wantNoTemps(t, dir)
}

// Probed with Node v26.5.1: renameSync onto a directory prints
// "Error: EISDIR: illegal operation on a directory, rename '<from>' -> '<to>'". The temp named is
// orch's own, and it is removed (PE-21: the JS names and leaves `<file>.tmp`).
func TestWritePrivateFileNamesAndRemovesTheTempOnAFailedRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "acks.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	err := WritePrivateFile(target, []byte("{}\n"))
	pattern := "^EISDIR: illegal operation on a directory, rename '" + regexp.QuoteMeta(target) + `\.tmp-[0-9]+' -> '` + regexp.QuoteMeta(target) + "'$"
	if err == nil || !regexp.MustCompile(pattern).MatchString(err.Error()) || Code(err) != "EISDIR" {
		t.Fatalf("WritePrivateFile onto a directory = %v (code %q), want a match for %s", err, Code(err), pattern)
	}
	wantNoTemps(t, dir)
}

func TestWritePrivateFileReportsAMissingDirectoryAgainstTheTempPattern(t *testing.T) {
	target := filepath.Join(t.TempDir(), "missing", "state.json")
	err := WritePrivateFile(target, nil)
	if want := "ENOENT: no such file or directory, open '" + target + ".tmp-*'"; err == nil || err.Error() != want {
		t.Fatalf("WritePrivateFile = %v, want %q", err, want)
	}
}

func TestMkdirPrivateCreatesTheLeafOwnerOnly(t *testing.T) {
	withUmask022(t)
	root := t.TempDir()
	leaf := filepath.Join(root, "parent", "runs")
	if err := MkdirPrivate(leaf); err != nil {
		t.Fatal(err)
	}
	wantMode(t, leaf, 0o700)
	wantMode(t, filepath.Dir(leaf), 0o755) // parents as MkdirAll makes them
}

func TestMkdirPrivateTightensAnExistingDirectoryOfOurs(t *testing.T) {
	withUmask022(t)
	leaf := filepath.Join(t.TempDir(), "runs")
	if err := os.Mkdir(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MkdirPrivate(leaf); err != nil {
		t.Fatal(err)
	}
	wantMode(t, leaf, 0o700)
}

func TestMkdirPrivateLeavesASymlinkedLeafsTargetAlone(t *testing.T) {
	withUmask022(t)
	root := t.TempDir()
	target := filepath.Join(root, "shared")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(root, "runs")
	if err := os.Symlink(target, leaf); err != nil {
		t.Fatal(err)
	}
	if err := MkdirPrivate(leaf); err != nil {
		t.Fatalf("MkdirPrivate = %v, want nil as MkdirAll gives (%v)", err, MkdirAll(leaf))
	}
	wantMode(t, target, 0o755)
}

func TestMkdirPrivateFailsAsMkdirAllDoes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "runs")
	writeFile(t, file)
	err := MkdirPrivate(file)
	if want := MkdirAll(file); err == nil || want == nil || err.Error() != want.Error() {
		t.Fatalf("MkdirPrivate = %v, want MkdirAll's %v", err, want)
	}
}

func TestAppendPrivateFileCreatesAnOwnerOnlyFileAndAppends(t *testing.T) {
	withUmask022(t)
	file := filepath.Join(t.TempDir(), "supervisor.log")
	for _, line := range []string{"one\n", "two\n"} {
		if err := AppendPrivateFile(file, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(file); string(data) != "one\ntwo\n" {
		t.Errorf("content = %q", data)
	}
	wantMode(t, file, 0o600)
}

func TestOpenPrivateAppendRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "supervisor.log")
	symlink(t, victim, link)
	f, err := OpenPrivateAppend(link)
	if err == nil {
		f.Close()
	}
	if want := "ELOOP: too many symbolic links encountered, open '" + link + "'"; err == nil || err.Error() != want {
		t.Fatalf("OpenPrivateAppend = %v, want %q", err, want)
	}
	if data, _ := os.ReadFile(victim); string(data) != "keep" {
		t.Errorf("symlink target = %q, want it untouched", data)
	}
}

func TestOpenPrivateAppendKeepsAppending(t *testing.T) {
	withUmask022(t)
	file := filepath.Join(t.TempDir(), "supervisor.log")
	if err := os.WriteFile(file, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := OpenPrivateAppend(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(file); string(data) != "old\nnew\n" {
		t.Errorf("content = %q", data)
	}
}

// Node gives every errno an err.code; Code knows only its table's, so for another errno the
// result approximates String(err) (S6a review F1). V8's own errors give String(err).
func TestErrorCodeIsTheCodeOrTheErrorString(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{syscall.EISDIR, "EISDIR"},
		{syscall.EDOM, "Error: " + syscall.EDOM.Error()},
		{jsjson.CannotAssign(7.0, "lane"), "TypeError: Cannot create property 'lane' on number '7'"},
		{jsjson.CannotReadOfNull("wake"), "TypeError: Cannot read properties of null (reading 'wake')"},
		{jsjson.ErrInvalidTime, "RangeError: Invalid time value"},
	} {
		if got := ErrorCode(tc.err); got != tc.want {
			t.Errorf("ErrorCode(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
