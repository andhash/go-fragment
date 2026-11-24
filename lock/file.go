// Package lock locks an idempotency key and a wallet address.
// Acquire is not reentrant in this process. Lock files are never deleted.
package lock

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// File locks names under root/locks.
type File struct {
	dir   string
	mu    sync.Mutex
	procs map[string]*procLock
}

type procLock struct {
	mu sync.Mutex
}

// Open creates the lock directory with mode 0700.
func Open(root string) (*File, error) {
	dir := filepath.Join(root, "locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return &File{dir: dir, procs: map[string]*procLock{}}, nil
}

// Acquire takes name. The same name in this process blocks until release.
// release is idempotent.
func (f *File) Acquire(ctx context.Context, name string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pl := f.entry(name)
	if err := waitMutex(ctx, &pl.mu); err != nil {
		return nil, err
	}
	path := filepath.Join(f.dir, name+".lock")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		pl.mu.Unlock()
		return nil, err
	}
	if err := fh.Chmod(0600); err != nil {
		fh.Close()
		pl.mu.Unlock()
		return nil, err
	}
	if err := lockFile(ctx, fh); err != nil {
		fh.Close()
		pl.mu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlockFile(fh)
			_ = fh.Close()
			pl.mu.Unlock()
		})
	}, nil
}

func (f *File) entry(name string) *procLock {
	f.mu.Lock()
	defer f.mu.Unlock()
	pl := f.procs[name]
	if pl == nil {
		pl = &procLock{}
		f.procs[name] = pl
	}
	return pl
}

func waitMutex(ctx context.Context, mu *sync.Mutex) error {
	for {
		if mu.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
