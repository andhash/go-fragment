package lock

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireIsNotReentrant(t *testing.T) {
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := l.Acquire(context.Background(), "id-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx, "id-a"); err == nil {
		t.Fatal("second acquire in the same process succeeded")
	}
	rel()
	rel2, err := l.Acquire(context.Background(), "id-a")
	if err != nil {
		t.Fatal(err)
	}
	rel2()
	if _, err := os.Stat(filepath.Join(l.dir, "id-a.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestLockBlocksOtherProcess(t *testing.T) {
	if os.Getenv("LOCK_CHILD") == "1" {
		l, err := Open(os.Getenv("LOCK_DIR"))
		if err != nil {
			os.Exit(3)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := l.Acquire(ctx, "held"); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := l.Acquire(context.Background(), "held")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestLockBlocksOtherProcess")
	cmd.Env = append(os.Environ(), "LOCK_CHILD=1", "LOCK_DIR="+dir)
	err = cmd.Run()
	if err == nil {
		t.Fatal("child acquired a lock this process holds")
	}
	rel()
}
