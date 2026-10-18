// Package knowledge stores curated memory in the owner's canonical Git sources.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrConflict = errors.New("memory revision conflict or uncommitted owned file")
	ErrInvalid  = errors.New("invalid memory input or scope")
)

func gitCommand(ctx context.Context, root string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "GIT_DIR" && key != "GIT_WORK_TREE" && key != "GIT_INDEX_FILE" {
			command.Env = append(command.Env, value)
		}
	}
	data, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s failed; check repository, identity and signing configuration", args[0])
	}
	return data, nil
}
func repositoryRoot(ctx context.Context, path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	out, err := gitCommand(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(out)) != path {
		return "", fmt.Errorf("knowledge root must be the Git repository root")
	}
	if _, err := gitCommand(ctx, path, "rev-parse", "--verify", "HEAD"); err != nil {
		return "", err
	}
	return path, nil
}
func revision(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func committed(ctx context.Context, root, path string) ([]byte, error) {
	return gitCommand(ctx, root, "show", "HEAD:"+path)
}
func trackedPaths(ctx context.Context, root, prefix string) ([]string, error) {
	raw, err := gitCommand(ctx, root, "ls-tree", "-rz", "--name-only", "HEAD", "--", prefix)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, path := range strings.Split(string(raw), "\x00") {
		if strings.HasSuffix(path, ".md") {
			paths = append(paths, path)
		}
	}
	return paths, nil
}
func fileCommit(ctx context.Context, root, path string) string {
	raw, _ := gitCommand(ctx, root, "log", "-1", "--format=%H", "--", path)
	return strings.TrimSpace(string(raw))
}
func lockRepository(ctx context.Context, root string) (func(), error) {
	value, err := gitCommand(ctx, root, "rev-parse", "--git-path", "cpa-memory.lock")
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(string(value))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("knowledge repository is locked; inspect a prior operation before retrying")
	}
	file.Close()
	return func() { _ = os.Remove(path) }, nil
}
func safeFile(root, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", ErrInvalid
	}
	path := root
	for _, part := range strings.Split(clean, string(os.PathSeparator)) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("knowledge paths must not contain symlinks")
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return path, nil
}
func commitFiles(ctx context.Context, root, message string, files map[string][]byte) error {
	previous := map[string][]byte{}
	existed := map[string]bool{}
	paths := []string{}
	for path := range files {
		full, err := safeFile(root, path)
		if err != nil {
			return err
		}
		status, err := gitCommand(ctx, root, "status", "--porcelain", "--untracked-files=all", "--", path)
		if err != nil {
			return err
		}
		if len(status) > 0 {
			return ErrConflict
		}
		data, err := os.ReadFile(full)
		if err == nil {
			previous[path] = data
			existed[path] = true
		} else if !os.IsNotExist(err) {
			return err
		}
		paths = append(paths, path)
	}
	restore := func() {
		for _, path := range paths {
			full, _ := safeFile(root, path)
			if existed[path] {
				_ = os.WriteFile(full, previous[path], 0600)
			} else {
				_ = os.Remove(full)
			}
		}
		_, _ = gitCommand(context.Background(), root, append([]string{"reset", "-q", "HEAD", "--"}, paths...)...)
	}
	for path, data := range files {
		full, _ := safeFile(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			restore()
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(full), ".memory-*.tmp")
		if err != nil {
			restore()
			return err
		}
		name := file.Name()
		if err = file.Chmod(0600); err == nil {
			_, err = file.Write(data)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, full)
		}
		if err != nil {
			os.Remove(name)
			restore()
			return err
		}
	}
	if _, err := gitCommand(ctx, root, append([]string{"add", "--"}, paths...)...); err != nil {
		restore()
		return err
	}
	// --only commits these files and leaves unrelated staged/unstaged work alone.
	if _, err := gitCommand(ctx, root, append([]string{"commit", "--quiet", "--only", "-m", message, "--"}, paths...)...); err != nil {
		// A cancellation can arrive just after Git commits. Do not undo a commit
		// whose tree already contains all requested bytes.
		complete := true
		for path, want := range files {
			got, e := committed(context.Background(), root, path)
			if e != nil || string(got) != string(want) {
				complete = false
				break
			}
		}
		if complete {
			return nil
		}
		restore()
		return err
	}
	return nil
}
