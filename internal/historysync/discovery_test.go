package historysync

import (
	"context"
	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootsHonorConfiguredHomes(t *testing.T) {
	home := t.TempDir()
	a := filepath.Join(home, "custom-codex")
	b := filepath.Join(home, "custom-claude")
	t.Setenv("CODEX_HOME", a)
	t.Setenv("CLAUDE_CONFIG_DIR", b)
	roots, err := DefaultRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	if roots[0].Path != filepath.Join(a, "sessions") || roots[2].Path != filepath.Join(b, "projects") {
		t.Fatalf("roots %+v", roots)
	}
	t.Setenv("CODEX_HOME", "relative")
	if _, err := DefaultRoots(home); err == nil {
		t.Fatal("relative configured root accepted")
	}
}
func TestCodexSegmentAndCompressionKeepIdentity(t *testing.T) {
	if _, err := decoderPath(); err != nil {
		t.Skip("zstd unavailable")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	os.Mkdir(root, 0700)
	sid := "11111111-1111-1111-1111-111111111111"
	name := "rollout-2026-10-18T00-00-00-" + sid + "_2.jsonl"
	raw := []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"parent\"}}\n{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":\"segmented-history\"}}\n")
	path := filepath.Join(root, name)
	os.WriteFile(path, raw, 0600)
	store, err := archive.OpenStore(filepath.Join(dir, "a.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	cfg := Config{Store: store, Device: "device", Roots: []Root{{"codex", root}}}
	r, err := Sync(context.Background(), cfg)
	if err != nil || r.Segments != 1 {
		t.Fatalf("plain %+v %v", r, err)
	}
	var native string
	store.DB.QueryRow("select value from record_facets where name='native.session_id'").Scan(&native)
	if native != sid {
		t.Fatalf("segment UUID %q", native)
	}
	decoder, _ := decoderPath()
	if out, err := exec.Command(decoder, "-q", "-f", path, "-o", path+".zst").CombinedOutput(); err != nil {
		t.Fatal("compress fixture", err, len(out))
	}
	os.Remove(path)
	r, err = Sync(context.Background(), cfg)
	if err != nil || r.Segments != 0 || r.Failures != 0 {
		t.Fatalf("compression replay %+v %v", r, err)
	}
	count := 0
	store.DB.QueryRow("select count(*) from records").Scan(&count)
	if count != 1 {
		t.Fatal(count)
	}
	// A corrupt compressed tail must never commit any of its decoded prefix.
	data, _ := os.ReadFile(path + ".zst")
	os.WriteFile(filepath.Join(root, "broken.jsonl.zst"), data[:len(data)-1], 0600)
	r, err = Sync(context.Background(), cfg)
	if err != nil || r.Failures != 1 || r.Segments != 0 {
		t.Fatalf("broken %+v %v", r, err)
	}
}
func TestExplicitRootSymlinkAndSettledProof(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.Mkdir(target, 0700)
	root := filepath.Join(dir, "linked")
	os.Symlink(target, root)
	path := filepath.Join(target, "s.jsonl")
	os.WriteFile(path, []byte("{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":\"proof\"}}\n"), 0600)
	store, err := archive.OpenStore(filepath.Join(dir, "a.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	now := time.Now().Add(time.Hour)
	cfg := Config{Store: store, Device: "d", Roots: []Root{{"codex", root}}, Now: func() time.Time { return now }}
	a, err := Sync(context.Background(), cfg)
	if err != nil || a.Failures != 0 || a.Segments != 1 {
		t.Fatalf("initial %+v %v", a, err)
	}
	b, err := Sync(context.Background(), cfg)
	if err != nil || b.UnchangedSkipped != 1 {
		t.Fatalf("proof %+v %v", b, err)
	}
	cfg.VerifyAll = true
	b, err = Sync(context.Background(), cfg)
	if err != nil || b.UnchangedSkipped != 0 {
		t.Fatalf("verify %+v %v", b, err)
	}
	cfg.VerifyAll = false
	now = now.Add(25 * time.Hour)
	b, err = Sync(context.Background(), cfg)
	if err != nil || b.UnchangedSkipped != 0 {
		t.Fatalf("expired %+v %v", b, err)
	}
	info, _ := os.Stat(path)
	raw, _ := os.ReadFile(path)
	raw[20] = 'X'
	os.WriteFile(path, raw, 0600)
	os.Chtimes(path, info.ModTime(), info.ModTime())
	// Force a distinct change stamp deterministically even on coarse filesystems.
	store.DB.Exec("update native_file_proofs set signature='invalidated'")
	b, err = Sync(context.Background(), cfg)
	if err != nil || b.Failures != 1 {
		t.Fatalf("rewritten %+v %v", b, err)
	}
}

func TestCompressedOnlyHistoryRetainsDecodedBytesAndPrefersPlainTwin(t *testing.T) {
	decoder, err := decoderPath()
	if err != nil {
		t.Skip("zstd unavailable")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	os.Mkdir(root, 0700)
	path := filepath.Join(root, "only.jsonl")
	raw := []byte("{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":\"compressed-only\"}}\n")
	os.WriteFile(path, raw, 0600)
	if err := exec.Command(decoder, "-q", "-f", path, "-o", path+".zst").Run(); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	store, err := archive.OpenStore(filepath.Join(dir, "a.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	cfg := Config{Store: store, Device: "d", Roots: []Root{{"codex", root}}}
	r, err := Sync(context.Background(), cfg)
	if err != nil || r.Failures != 0 || r.Segments != 1 {
		t.Fatalf("compressed %+v %v", r, err)
	}
	var rid string
	store.DB.QueryRow("select request_id from records").Scan(&rid)
	record, err := store.Request(context.Background(), rid)
	if err != nil {
		t.Fatal(err)
	}
	var env nativehistory.Envelope
	if err := json.Unmarshal(record.OriginalRequest, &env); err != nil || len(env.Lines) != 1 || env.Lines[0].Raw != string(raw) {
		t.Fatal("decoded original lost")
	}
	os.WriteFile(path, raw, 0600)
	os.WriteFile(path+".zst", []byte("broken compression in progress"), 0600)
	r, err = Sync(context.Background(), cfg)
	if err != nil || r.Failures != 0 || r.Segments != 0 {
		t.Fatalf("plain twin %+v %v", r, err)
	}
}

func TestClaudeSubagentSidecarKeepsParentKindsSeparate(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "projects")
	folder := filepath.Join(root, "project", "parent-session", "subagents")
	os.MkdirAll(folder, 0700)
	path := filepath.Join(folder, "agent-child.jsonl")
	os.WriteFile(path, []byte("{\"type\":\"user\",\"sessionId\":\"parent-session\",\"message\":{\"content\":\"subagent question\"}}\n"), 0600)
	os.WriteFile(strings.TrimSuffix(path, ".jsonl")+".meta.json", []byte(`{"agentType":"Explore","parentAgentId":"parent-agent","description":"Inspect fixtures"}`), 0600)
	store, err := archive.OpenStore(filepath.Join(dir, "a.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	report, err := Sync(context.Background(), Config{Store: store, Device: "d", Roots: []Root{{"claude-code", root}}})
	if err != nil || report.Segments != 1 {
		t.Fatalf("sync %+v %v", report, err)
	}
	var kind, parent string
	store.DB.QueryRow("select value from record_facets where name='native.agent_kind'").Scan(&kind)
	store.DB.QueryRow("select value from record_facets where name='native.parent_agent_id'").Scan(&parent)
	if kind != "Explore" || parent != "parent-agent" {
		t.Fatalf("kind=%q parent=%q", kind, parent)
	}
}
