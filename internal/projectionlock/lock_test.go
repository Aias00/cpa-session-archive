package projectionlock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExclusiveOwnerSurvivesAliasesAndRelease(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "memory.sqlite")
	first, err := Acquire(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{database, filepath.Join(alias, "memory.sqlite")} {
		if duplicate, err := Acquire(path); err == nil {
			duplicate.Close()
			t.Fatal("two owners acquired one projection")
		}
	}
	other, err := Acquire(filepath.Join(dir, "other.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	other.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(database)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	info, err := os.Stat(database + ".owner.lock")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("owner lock was removed or exposed")
	}
}

func TestOwnerLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "private")
	if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "memory.sqlite")
	if err := os.Symlink(target, path+".owner.lock"); err != nil {
		t.Fatal(err)
	}
	if lock, err := Acquire(path); err == nil {
		lock.Close()
		t.Fatal("owner lock followed a symlink")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "fixture" {
		t.Fatal("owner lock modified its symlink target")
	}
}

func TestOwnerLockRejectsHardLinkedDatabase(t *testing.T) {
	dir := t.TempDir()
	path, alias := filepath.Join(dir, "memory.sqlite"), filepath.Join(dir, "alias.sqlite")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, alias} {
		if lock, err := Acquire(candidate); err == nil {
			lock.Close()
			t.Fatal("hard-link aliases bypassed projection ownership")
		}
	}
}
