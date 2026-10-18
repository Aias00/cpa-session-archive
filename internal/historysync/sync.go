package historysync

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
	"time"
)

type Root struct{ Client, Path string }
type Config struct {
	Store    *archive.Store
	Device   string
	Roots    []Root
	Progress func(Report)
}
type Report struct {
	Files        int         `json:"files"`
	Segments     int         `json:"segments"`
	Existing     int         `json:"existing_segments"`
	Bytes        int64       `json:"bytes"`
	PendingTails int         `json:"pending_tails"`
	Failures     int         `json:"failures"`
	Errors       []FileError `json:"errors,omitempty"`
}
type FileError struct {
	Client string `json:"client"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type checkpoint struct {
	Offset  int64
	Digest  string
	Context nativehistory.Context
}

const maxLine = 64 << 20
const segmentBytes = 1 << 20
const segmentLines = 64

var codexFileSession = regexp.MustCompile(`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// Sync preserves complete original records, checkpointing only after archive
// commit. It reports per-file failures while allowing independent files to progress.
func Sync(ctx context.Context, cfg Config) (Report, error) {
	var report Report
	if cfg.Store == nil || strings.TrimSpace(cfg.Device) == "" {
		return report, fmt.Errorf("store and explicit device identity are required")
	}
	_, err := cfg.Store.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS native_sync_files(source_key TEXT PRIMARY KEY,offset INTEGER NOT NULL,prefix_sha256 TEXT NOT NULL,context_json TEXT NOT NULL)`)
	if err != nil {
		return report, err
	}
	remotes := map[string]string{}
	for _, root := range cfg.Roots {
		if root.Client != "codex" && root.Client != "claude-code" && root.Client != "kiro-cli" {
			return report, fmt.Errorf("unsupported native client")
		}
		info, err := os.Lstat(root.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return report, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return report, fmt.Errorf("history root must be a real directory")
		}
		err = filepath.WalkDir(root.Path, func(path string, entry os.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				report.Failures++
				report.Errors = append(report.Errors, FileError{root.Client, path, "cannot enumerate source"})
				return nil
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".jsonl" && !(root.Client == "kiro-cli" && (ext == ".json" || ext == ".history")) {
				return nil
			}
			// A move from Codex active to archived history preserves source identity.
			// Native session filenames are IDs; client and device keep their namespaces apart.
			key := cfg.Device + "\x00" + root.Client + "\x00" + filepath.Base(path)
			report.Files++
			if err := syncFile(ctx, cfg, root.Client, path, key, remotes, &report); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				report.Failures++
				report.Errors = append(report.Errors, FileError{root.Client, path, err.Error()})
			}
			if cfg.Progress != nil {
				cfg.Progress(report)
			}
			return nil
		})
		if err != nil {
			return report, err
		}
	}
	return report, nil
}
func readLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(out)+len(part) > maxLine {
			return nil, fmt.Errorf("native record exceeds 64 MiB; source not advanced")
		}
		out = append(out, part...)
		if err != bufio.ErrBufferFull {
			// A complete final JSON value is safe even without a trailing newline.
			// Incomplete values remain uncommitted until the client finishes them.
			if err == io.EOF && len(out) > 0 && (json.Valid(out) || len(bytes.TrimSpace(out)) == 0) {
				return out, nil
			}
			return out, err
		}
	}
}
func digest(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
func id(parts ...string) string {
	s := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(s[:])
}
func loadCheckpoint(ctx context.Context, store *archive.Store, key string) (checkpoint, error) {
	var cp checkpoint
	var raw string
	err := store.DB.QueryRowContext(ctx, `SELECT offset,prefix_sha256,context_json FROM native_sync_files WHERE source_key=?`, key).Scan(&cp.Offset, &cp.Digest, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cp, nil
	}
	if err != nil {
		return cp, err
	}
	if json.Unmarshal([]byte(raw), &cp.Context) != nil {
		return cp, fmt.Errorf("invalid native checkpoint context")
	}
	return cp, nil
}
func saveCheckpoint(ctx context.Context, store *archive.Store, key string, cp checkpoint) error {
	raw, _ := json.Marshal(cp.Context)
	_, err := store.DB.ExecContext(ctx, `INSERT INTO native_sync_files(source_key,offset,prefix_sha256,context_json) VALUES(?,?,?,?) ON CONFLICT(source_key) DO UPDATE SET offset=excluded.offset,prefix_sha256=excluded.prefix_sha256,context_json=excluded.context_json`, key, cp.Offset, cp.Digest, string(raw))
	return err
}
func syncFile(ctx context.Context, cfg Config, client, path, key string, remotes map[string]string, report *Report) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open source")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a readable regular file")
	}
	cp, err := loadCheckpoint(ctx, cfg.Store, key)
	if err != nil {
		return err
	}
	auxiliary := filepath.Ext(path) != ".jsonl"
	if auxiliary {
		return syncAuxiliary(ctx, cfg, client, path, key, file, info.Size(), cp, remotes, report)
	}
	if info.Size() < cp.Offset {
		return fmt.Errorf("source truncated; retained history preserved, reconciliation required")
	}
	h := sha256.New()
	if cp.Offset > 0 {
		if _, err = io.CopyN(h, file, cp.Offset); err != nil {
			return fmt.Errorf("cannot verify committed source prefix")
		}
		if digest(h) != cp.Digest {
			return fmt.Errorf("source prefix rewritten; retained history preserved, reconciliation required")
		}
	}
	if info.Size() == cp.Offset {
		return nil
	}
	c := cp.Context
	if c.SessionID == "" {
		c.SessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	// Kiro stores project/session context in its sibling metadata file.
	if client == "kiro-cli" {
		meta, err := os.Open(strings.TrimSuffix(path, ".jsonl") + ".json")
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(meta, maxLine+1))
			meta.Close()
			if len(raw) <= maxLine {
				nativehistory.Inspect(client, raw, &c)
			}
		}
	}
	// Inspect complete new records before writing so the same file consistently
	// chooses response items over duplicate Codex event messages.
	r := bufio.NewReaderSize(io.LimitReader(file, info.Size()-cp.Offset), 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, readErr := readLine(r)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if readErr == io.EOF {
			break
		}
		nativehistory.Inspect(client, raw, &c)
	}
	enrich(ctx, client, path, &c, remotes)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = info.ModTime().UTC()
	}
	if _, err = file.Seek(cp.Offset, io.SeekStart); err != nil {
		return err
	}
	r = bufio.NewReaderSize(io.LimitReader(file, info.Size()-cp.Offset), 64<<10)
	var lines []nativehistory.Line
	start := cp.Offset
	offset := cp.Offset
	size := 0
	flush := func() error {
		if len(lines) == 0 {
			return nil
		}
		env := nativehistory.Envelope{Version: 1, Client: client, Context: c, Lines: lines}
		if err := commitSegment(ctx, cfg, key, path, env, start, offset, report); err != nil {
			return err
		}
		if err := saveCheckpoint(ctx, cfg.Store, key, checkpoint{offset, digest(h), c}); err != nil {
			return err
		}
		report.Bytes += offset - start
		lines = nil
		start = offset
		size = 0
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, readErr := readLine(r)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if readErr == io.EOF {
			if len(bytes.TrimSpace(raw)) > 0 {
				report.PendingTails++
			}
			break
		}
		if size > 0 && (len(lines) >= segmentLines || size+len(raw) > segmentBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		lines = append(lines, nativehistory.Line{Offset: offset, Raw: string(raw)})
		offset += int64(len(raw))
		size += len(raw)
		h.Write(raw)
	}
	return flush()
}
func enrich(ctx context.Context, client, path string, c *nativehistory.Context, remotes map[string]string) {
	if client == "codex" {
		// Fork rollouts can contain only an inherited parent session_meta.
		// The native file UUID identifies the independently retained thread.
		if match := codexFileSession.FindStringSubmatch(filepath.Base(path)); len(match) == 2 && match[1] != c.SessionID {
			if c.ParentID == "" && len(c.SessionID) == 36 {
				c.ParentID = c.SessionID
			}
			c.SessionID = match[1]
		}
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if client == "claude-code" && strings.Contains(filepath.ToSlash(path), "/subagents/") {
		c.Branch = stem
	}
	if c.CWD != "" && c.GitRemote == "" {
		remote, ok := remotes[c.CWD]
		if !ok {
			raw, err := exec.CommandContext(ctx, "git", "-C", c.CWD, "config", "--get", "remote.origin.url").Output()
			if err == nil {
				remote = strings.TrimSpace(string(raw))
			}
			remotes[c.CWD] = remote
		}
		c.GitRemote = remote
	}
}
func syncAuxiliary(ctx context.Context, cfg Config, client, path, key string, file *os.File, size int64, cp checkpoint, remotes map[string]string, report *Report) error {
	if size > maxLine {
		return fmt.Errorf("auxiliary snapshot exceeds 64 MiB; source not advanced")
	}
	raw, err := io.ReadAll(io.LimitReader(file, size))
	if err != nil {
		return fmt.Errorf("cannot read auxiliary source")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if cp.Digest == hash && cp.Offset == int64(len(raw)) {
		return nil
	}
	c := nativehistory.Context{SessionID: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	nativehistory.Inspect(client, raw, &c)
	enrich(ctx, client, path, &c, remotes)
	if c.CreatedAt.IsZero() {
		info, _ := file.Stat()
		c.CreatedAt = info.ModTime().UTC()
	}
	e := nativehistory.Envelope{Version: 1, Client: client, Context: c, Lines: []nativehistory.Line{{Offset: 0, Raw: string(raw)}}}
	if err := commitSegment(ctx, cfg, key, path, e, 0, int64(len(raw)), report); err != nil {
		return err
	}
	report.Bytes += int64(len(raw))
	return saveCheckpoint(ctx, cfg.Store, key, checkpoint{int64(len(raw)), hash, c})
}
func commitSegment(ctx context.Context, cfg Config, key, path string, e nativehistory.Envelope, start, end int64, report *Report) error {
	h := sha256.New()
	for _, l := range e.Lines {
		h.Write([]byte(l.Raw))
	}
	sourceID := "native:" + id(key, fmt.Sprint(start), fmt.Sprint(end), digest(h))
	var exists bool
	if err := cfg.Store.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM records WHERE request_id=?)`, sourceID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		report.Existing++
		return nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	c := e.Context
	session := "native:" + id(cfg.Device, e.Client, c.SessionID, c.Branch)
	project := "path:" + cfg.Device + ":" + c.CWD
	if c.CWD == "" {
		project = "native:" + cfg.Device + ":unassigned"
	}
	if c.GitRemote != "" {
		project = "git:" + c.GitRemote
	}
	facets := map[string][]string{"client": {e.Client}, "project.id": {project}, "request.kind": {"native-history"}, "thread.source": {"native-history"}, "session.id": {session}, "native.device": {cfg.Device}, "native.session_id": {c.SessionID}}
	if c.CWD != "" {
		facets["project.path"] = []string{c.CWD}
		facets["project.name"] = []string{filepath.Base(c.CWD)}
	}
	if c.GitRemote != "" {
		facets["git.remote"] = []string{c.GitRemote}
	}
	if c.ParentID != "" {
		facets["native.parent_id"] = []string{c.ParentID}
	}
	if c.Branch != "" {
		facets["native.branch"] = []string{c.Branch}
	}
	stamp := c.CreatedAt
	for _, line := range e.Lines {
		var m map[string]any
		if json.Unmarshal([]byte(line.Raw), &m) == nil {
			t := timestamp(m["timestamp"])
			if t.IsZero() {
				t = timestamp(object(object(m["data"])["meta"])["timestamp"])
			}
			if !t.IsZero() {
				stamp = t
				break
			}
		}
	}
	doc := nativehistory.Normalize(e)
	summary, preview := "", ""
	for _, m := range doc.Messages {
		value := []rune(m.Text)
		if len(value) > 160 {
			value = value[:160]
		}
		if m.Role == "user" && summary == "" {
			summary = string(value)
		}
		if m.Role == "assistant" {
			preview = string(value)
		}
	}
	r := archive.Record{RequestID: sourceID, SessionID: session, SourceFormat: nativehistory.Format, Client: e.Client, RequestKind: "native-history", StartedAt: stamp, CompletedAt: stamp, Outcome: "imported", OriginalRequest: raw, Model: c.Model, RequestedModel: c.Model, Summary: summary, ResponsePreview: preview, Facets: facets, Metadata: map[string]any{"native_device": cfg.Device, "native_client": e.Client, "native_path": path, "native_start": start, "native_end": end, "native_sha256": digest(h), "native_session_id": c.SessionID, "native_parent_id": c.ParentID, "native_branch": c.Branch}}
	if err := cfg.Store.PutBatch([]archive.Record{r}); err != nil {
		return err
	}
	report.Segments++
	return nil
}

func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func timestamp(v any) time.Time {
	s, _ := v.(string)
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
