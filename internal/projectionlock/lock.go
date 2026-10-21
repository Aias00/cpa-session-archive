// Package projectionlock enforces a single daemon owner for a local projection.
package projectionlock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire resolves directory/file symlinks before choosing a stable lock path.
// Keep the lock file after release: unlinking would allow two locked inodes.
func Acquire(databasePath string) (*Lock, error) {
	if databasePath == "" || databasePath == ":memory:" {
		return nil, errors.New("a local projection path is required")
	}
	abs, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, errors.New("invalid projection path")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, errors.New("projection directory unavailable")
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
		if parentErr != nil {
			return nil, errors.New("projection directory unavailable")
		}
		canonical, err = filepath.Join(parent, filepath.Base(abs)), nil
	}
	if err != nil {
		return nil, errors.New("projection path unavailable")
	}
	if info, err := os.Stat(canonical); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			return nil, errors.New("memory projection must not have hard links")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("projection path unavailable")
	}
	file, err := os.OpenFile(canonical+".owner.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("projection owner lock unavailable")
	}
	fail := func(err error) (*Lock, error) { file.Close(); return nil, err }
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fail(errors.New("projection owner lock must be a regular file"))
	}
	if err := file.Chmod(0600); err != nil {
		return fail(errors.New("projection owner lock must be private"))
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(errors.New("memory projection is already owned by another process"))
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Close() error {
	l.once.Do(func() { l.err = errors.Join(syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN), l.file.Close()) })
	return l.err
}
