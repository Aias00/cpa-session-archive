package historysync

import (
	"context"
	"cpa-session-archive/internal/nativehistory"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cpa-session-archive/internal/archive"
)

func TestSyncReplayAppendRewriteAndActiveTail(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	os.Mkdir(root, 0700)
	path := filepath.Join(root, "thread.jsonl")
	first := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread\",\"cwd\":\"/project\"}}\n{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":\"hello native\"}}\n"
	os.WriteFile(path, []byte(first), 0600)
	store, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	cfg := Config{Store: store, Device: "fixture-device", Roots: []Root{{Client: "codex", Path: root}}}
	run := func() Report {
		t.Helper()
		r, e := Sync(context.Background(), cfg)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	a := run()
	if a.Segments != 1 || a.Bytes != int64(len(first)) || a.Failures != 0 {
		t.Fatalf("initial %+v", a)
	}
	b := run()
	if b.Segments != 0 || b.Bytes != 0 {
		t.Fatalf("replay %+v", b)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("{\"type\":\"response_item\"")
	f.Close()
	c := run()
	if c.Segments != 0 || c.PendingTails != 1 {
		t.Fatalf("tail %+v", c)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(",\"payload\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":\"done\"}}\n")
	f.Close()
	d := run()
	if d.Segments != 1 || d.Failures != 0 {
		t.Fatalf("append %+v", d)
	}
	data, _ := os.ReadFile(path)
	data[10] = 'X'
	os.WriteFile(path, data, 0600)
	e := run()
	if e.Failures != 1 || e.Segments != 0 {
		t.Fatalf("rewrite %+v", e)
	}
	var count int
	store.DB.QueryRow("select count(*) from records").Scan(&count)
	if count != 2 {
		t.Fatalf("records %d", count)
	}
}

func TestSyncIndependentClientsMoveMalformedAndCheckpointReplay(t *testing.T) {
	dir := t.TempDir()
	store, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	a := filepath.Join(dir, "active")
	b := filepath.Join(dir, "archived")
	os.Mkdir(a, 0700)
	os.Mkdir(b, 0700)
	path := filepath.Join(a, "same.jsonl")
	raw := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"same\"}}\n{broken}\n"
	os.WriteFile(path, []byte(raw), 0600)
	cfg := Config{Store: store, Device: "device", Roots: []Root{{"codex", a}, {"codex", b}, {"claude-code", b}}}
	r, err := Sync(context.Background(), cfg)
	if err != nil || r.Segments != 1 {
		t.Fatalf("initial %+v %v", r, err)
	}
	// Simulate archive commit followed by a crash before the checkpoint commit.
	store.DB.Exec("delete from native_sync_files")
	r, err = Sync(context.Background(), cfg)
	if err != nil || r.Segments != 0 || r.Existing != 1 {
		t.Fatalf("recovery %+v %v", r, err)
	}
	os.Rename(path, filepath.Join(b, "same.jsonl"))
	r, err = Sync(context.Background(), cfg)
	if err != nil || r.Segments != 1 || r.Failures != 0 {
		t.Fatalf("move/client identity %+v %v", r, err)
	}
	var count int
	store.DB.QueryRow("select count(*) from records").Scan(&count)
	if count != 2 {
		t.Fatalf("records %d", count)
	}
}

func TestSyncRejectsSymlinksAndKeepsKiroSidecars(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "kiro")
	os.Mkdir(root, 0700)
	os.WriteFile(filepath.Join(root, "session.json"), []byte(`{"session_id":"session","cwd":"/test","created_at":"2026-10-18T00:00:00Z"}`), 0600)
	os.WriteFile(filepath.Join(root, "session.history"), []byte("legacy prompt"), 0600)
	os.Symlink(filepath.Join(root, "session.history"), filepath.Join(root, "escape.jsonl"))
	store, err := archive.OpenStore(filepath.Join(dir, "a.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	r, err := Sync(context.Background(), Config{Store: store, Device: "d", Roots: []Root{{"kiro-cli", root}}})
	if err != nil || r.Segments != 2 || r.Files != 2 {
		t.Fatalf("sidecars %+v %v", r, err)
	}
	r, err = Sync(context.Background(), Config{Store: store, Device: "d", Roots: []Root{{"kiro-cli", root}}})
	if err != nil || r.Segments != 0 {
		t.Fatalf("replay %+v %v", r, err)
	}
}

func TestSyncBoundedSegmentsRetainRawAndFinalJSON(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	os.Mkdir(root, 0700)
	raw := ""
	for i := 0; i < 130; i++ {
		raw += "{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":\"repeat\"}}\n"
	}
	raw += `{"type":"response_item","payload":{"type":"reasoning","encrypted_content":"retained-opaque"}}`
	os.WriteFile(filepath.Join(root, "s.jsonl"), []byte(raw), 0600)
	store, err := archive.OpenStore(filepath.Join(dir, "a.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	cfg := Config{Store: store, Device: "d", Roots: []Root{{"codex", root}}}
	r, err := Sync(context.Background(), cfg)
	if err != nil || r.Segments != 3 || r.Bytes != int64(len(raw)) || r.PendingTails != 0 {
		t.Fatalf("bounded %+v %v", r, err)
	}
	var ids []string
	rows, _ := store.DB.Query("select request_id from records order by id")
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var reconstructed string
	for _, id := range ids {
		record, e := store.Request(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		var env nativehistory.Envelope
		if e = json.Unmarshal(record.OriginalRequest, &env); e != nil {
			t.Fatal(e)
		}
		if len(env.Lines) > 64 {
			t.Fatal("unbounded segment")
		}
		for _, line := range env.Lines {
			reconstructed += line.Raw
		}
	}
	if reconstructed != raw {
		t.Fatal("original bytes changed")
	}
	r, err = Sync(context.Background(), cfg)
	if err != nil || r.Segments != 0 || r.Bytes != 0 {
		t.Fatalf("final replay %+v %v", r, err)
	}
}
