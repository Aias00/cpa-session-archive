package historysync

import (
	"context"
	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/nativehistory"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseCapturePrefersV2ReplaysAndRetainsUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE session(id TEXT,parent_id TEXT,directory TEXT,title TEXT,time_created INTEGER);CREATE TABLE session_v2(id TEXT,parent_id TEXT,directory TEXT,title TEXT,time_created INTEGER);CREATE TABLE message(id TEXT,session_id TEXT,time_created INTEGER,data TEXT);CREATE TABLE part(id TEXT,message_id TEXT,time_created INTEGER,data TEXT);CREATE TABLE session_message(id TEXT,session_id TEXT,type TEXT,seq INTEGER,time_created INTEGER,data TEXT);
 INSERT INTO session VALUES('s',NULL,'/project','old',1000);INSERT INTO session_v2 VALUES('s',NULL,'/project','new',1000);
 INSERT INTO message VALUES('old','s',1000,'{"role":"user"}');INSERT INTO part VALUES('p','old',1000,'{"type":"text","text":"duplicate legacy"}');
 INSERT INTO session_message VALUES('new','s','user',1,1000,'{"text":"database needle"}');`)
	if err != nil {
		t.Fatal(err)
	}
	store, err := archive.OpenStore(filepath.Join(dir, "stage.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	cfg := Config{Store: store, Device: "device", Roots: []Root{{"opencode", path}}}
	first, err := Sync(context.Background(), cfg)
	if err != nil || first.Failures != 0 || first.Segments != 1 {
		t.Fatalf("first %+v %v", first, err)
	}
	again, err := Sync(context.Background(), cfg)
	if err != nil || again.Segments != 0 {
		t.Fatalf("replay %+v %v", again, err)
	}
	db.Exec(`UPDATE session_message SET data='{"text":"updated needle"}' WHERE id='new'`)
	update, err := Sync(context.Background(), cfg)
	if err != nil || update.Segments != 1 {
		t.Fatalf("update %+v %v", update, err)
	}
	var count int
	store.DB.QueryRow("select count(*) from records").Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
	if _, err := db.Exec(`UPDATE session_v2 SET title='corrected' WHERE id='s'`); err != nil {
		t.Fatal(err)
	}
	revised, err := Sync(context.Background(), cfg)
	if err != nil || revised.Segments != 1 {
		t.Fatalf("metadata update %+v %v", revised, err)
	}
	store.DB.QueryRow("select count(*) from records").Scan(&count)
	if count != 3 {
		t.Fatal("metadata revision was not archived", count)
	}
	if _, err := db.Exec(`UPDATE session_message SET data='broken' WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	bad, err := Sync(context.Background(), cfg)
	if err != nil || bad.Failures != 1 || bad.Segments != 0 {
		t.Fatalf("bad row %+v %v", bad, err)
	}
}

func TestAdditionalJSONLClientsReplayAndKeepChildIdentity(t *testing.T) {
	for _, client := range []string{"qoder", "workbuddy", "codebuddy"} {
		t.Run(client, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "projects")
			path := filepath.Join(root, "project", "parent", "subagents", "child.jsonl")
			writeHistory(t, path, "{\"type\":\"message\",\"role\":\"user\",\"sessionId\":\"parent\",\"content\":\"needle\"}\n")
			store, err := archive.OpenStore(filepath.Join(dir, "stage.sqlite"), false)
			if err != nil {
				t.Fatal(err)
			}
			defer store.DB.Close()
			cfg := Config{Store: store, Device: "d", Roots: []Root{{client, root}}}
			r, err := Sync(context.Background(), cfg)
			if err != nil || r.Segments != 1 || r.Failures != 0 {
				t.Fatalf("%+v %v", r, err)
			}
			var parent, branch string
			store.DB.QueryRow("select value from record_facets where name='native.parent_id'").Scan(&parent)
			store.DB.QueryRow("select value from record_facets where name='native.branch'").Scan(&branch)
			if parent != "parent" || branch != "child" {
				t.Fatal(parent, branch)
			}
			r, err = Sync(context.Background(), cfg)
			if err != nil || r.Segments != 0 {
				t.Fatalf("replay %+v %v", r, err)
			}
		})
	}
}

func writeHistory(t *testing.T, path, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyZCodeKeepsToolPartsAndSourceDatabaseUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zcode.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE session(id TEXT,parent_id TEXT,directory TEXT,title TEXT,time_created INTEGER);CREATE TABLE message(id TEXT,session_id TEXT,time_created INTEGER,data TEXT);CREATE TABLE part(id TEXT,message_id TEXT,time_created INTEGER,data TEXT);
 INSERT INTO session VALUES('s','parent','/project','title',1000);INSERT INTO message VALUES('m','s',1000,'{"role":"assistant","modelId":"model-a"}');INSERT INTO part VALUES('p','m',1000,'{"type":"tool","tool":"shell","state":{"input":{},"output":"legacy result"}}');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := archive.OpenStore(filepath.Join(dir, "stage.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	r, err := Sync(context.Background(), Config{Store: store, Device: "d", Roots: []Root{{"zcode", path}}})
	if err != nil || r.Failures != 0 || r.Segments != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source database changed")
	}
	var id string
	if err := store.DB.QueryRow("select request_id from records").Scan(&id); err != nil {
		t.Fatal(err)
	}
	record, err := store.Request(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var env nativehistory.Envelope
	if err := json.Unmarshal(record.OriginalRequest, &env); err != nil {
		t.Fatal(err)
	}
	d := nativehistory.Normalize(env)
	if len(d.Messages) != 2 || d.Messages[1].Text != "legacy result" || env.Context.ParentID != "parent" {
		t.Fatal(d, env.Context)
	}
}
