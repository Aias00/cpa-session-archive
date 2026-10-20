package historysync

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultRoots respects client configuration; relative environment paths would
// otherwise depend on launchd's working directory and read the wrong profile.
func DefaultRoots(home string) ([]Root, error) {
	resolve := func(key, fallback string) (string, error) {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			return filepath.Join(home, fallback), nil
		}
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s must be absolute", key)
		}
		return filepath.Clean(value), nil
	}
	codex, err := resolve("CODEX_HOME", ".codex")
	if err != nil {
		return nil, err
	}
	claude, err := resolve("CLAUDE_CONFIG_DIR", ".claude")
	if err != nil {
		return nil, err
	}
	roots := []Root{{"codex", filepath.Join(codex, "sessions")}, {"codex", filepath.Join(codex, "archived_sessions")}, {"claude-code", filepath.Join(claude, "projects")}, {"kiro-cli", filepath.Join(home, ".kiro/sessions/cli")}}
	for _, client := range []struct{ id, env, dir string }{{"qoder", "QODER_CONFIG_DIR", ".qoder"}, {"qoder-cn", "QODERCN_CONFIG_DIR", ".qoder-cn"}, {"workbuddy", "WORKBUDDY_CONFIG_DIR", ".workbuddy"}, {"codebuddy", "CODEBUDDY_CONFIG_DIR", ".codebuddy"}} {
		dir, err := resolve(client.env, client.dir)
		if err != nil {
			return nil, err
		}
		roots = append(roots, Root{client.id, filepath.Join(dir, "projects")})
	}
	data, err := resolve("XDG_DATA_HOME", ".local/share")
	if err != nil {
		return nil, err
	}
	roots = append(roots, Root{"opencode", filepath.Join(data, "opencode/opencode.db")}, Root{"zcode", filepath.Join(home, ".zcode/cli/db/db.sqlite")})
	return roots, nil
}

// Rollout/segment recognition follows Magpie's MIT-licensed sessions reader.
var rolloutSession = regexp.MustCompile(`^rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-([0-9A-Za-z-]+?)(?:_[0-9A-Za-z-]+)?\.jsonl$`)

func logicalName(path string) string { return strings.TrimSuffix(filepath.Base(path), ".zst") }
func fileSession(path string) string {
	name := logicalName(path)
	if m := rolloutSession.FindStringSubmatch(name); len(m) == 2 {
		return m[1]
	}
	if m := codexFileSession.FindStringSubmatch(name); len(m) == 2 {
		return m[1]
	}
	return ""
}
func decoderPath() (string, error) {
	if p, err := exec.LookPath("zstd"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/zstd", "/usr/local/bin/zstd"} {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("zstd executable is required for compressed histories")
}

// A complete frame is verified before any source/checkpoint commit. Decoding
// streams to a private spool so a corrupt trailer cannot acknowledge a prefix.
func decodeHistory(ctx context.Context, path string) (*os.File, func(), error) {
	decoder, err := decoderPath()
	if err != nil {
		return nil, nil, err
	}
	file, err := os.CreateTemp("", "cpa-history-decoded-*.jsonl")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { file.Close(); os.Remove(file.Name()) }
	command := exec.CommandContext(ctx, decoder, "-q", "-d", "-c", "--", path)
	command.Stdout = file
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("compressed history frame is incomplete or invalid")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return file, cleanup, nil
}

func supportedClient(client string) bool {
	switch client {
	case "codex", "claude-code", "kiro-cli", "qoder", "qoder-cn", "workbuddy", "codebuddy", "opencode", "zcode":
		return true
	}
	return false
}
