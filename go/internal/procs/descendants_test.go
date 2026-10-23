//go:build linux || darwin

package procs

import (
	"os"
	"reflect"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestLiveDescendantWalkExcludesZombies(t *testing.T) {
	entries := []processEntry{{pid: 2, parent: 1}, {pid: 3, parent: 2, zombie: true}, {pid: 4, parent: 3}, {pid: 9, parent: 8}, {pid: 1, parent: 4}}
	if got := liveDescendants(1, entries); !reflect.DeepEqual(got, []int{2, 4}) {
		t.Errorf("live descendants %v", got)
	}
}

func TestLiveDescendantsExcludesRealZombie(t *testing.T) {
	child := startUnreaped(t, "exit 0")
	if err := waitExited(child.pid); err != nil {
		t.Fatal(err)
	}
	pids, err := LiveDescendants(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(pids, child.pid) {
		t.Errorf("zombie %d counted as live: %v", child.pid, pids)
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(child.pid, &status, 0, nil); err != nil {
		t.Fatal(err)
	}
	child.reaped = true
}

func TestLiveDescendantsRealGrandchild(t *testing.T) {
	input, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer release.Close()
	ids := make(chan int, 2)
	lines := &Lines{OnLine: func(line string) {
		id, err := strconv.Atoi(line)
		if err == nil {
			ids <- id
		}
	}}
	child, err := Start(Spec{Name: "/bin/bash", Args: []string{"-c", `exec 3<&0; /bin/bash -c 'echo $$; /bin/bash -c "echo \$\$; read -r release <&3" & wait' & wait`}, Stdin: input, Stdout: lines, Grace: time.Second, OwnGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Signal(9) }) // tracked fake only; Signal is a no-op after reap
	var want []int
	for i := 0; i < 2; i++ {
		select {
		case id := <-ids:
			want = append(want, id)
		case <-time.After(5 * time.Second):
			t.Fatal("tree did not become ready")
		}
	}
	slices.Sort(want)
	got, err := LiveDescendants(child.Pid())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("descendants %v, want %v", got, want)
	}
	if _, err := release.WriteString("release\n"); err != nil {
		t.Fatal(err)
	}
	code, err := child.Wait()
	if err != nil || code == nil || *code != 0 {
		t.Fatalf("wait %v %v", code, err)
	}
	got, err = LiveDescendants(child.Pid())
	if err != nil || len(got) != 0 {
		t.Errorf("exited tree %v, %v", got, err)
	}
}
