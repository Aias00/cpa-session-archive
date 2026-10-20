package archive

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

func TestHistoricalSSENormalizationSkipsDeletedWorkItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	store, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	store.DB.SetMaxOpenConns(1)
	if err := store.PutBatch([]Record{{RequestID: "orphan", SessionID: "s"}, {RequestID: "during", SessionID: "s"}, {RequestID: "live", SessionID: "s"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`DELETE FROM records WHERE request_id='orphan';
 ALTER TABLE session_indexed_requests RENAME TO sse_work_data;
 CREATE VIEW session_indexed_requests AS SELECT CASE WHEN sse_delete_probe(request_id)=1 THEN request_id ELSE request_id END AS request_id,session_id FROM sse_work_data;`); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite3", path+"?_busy_timeout=0&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.Ping(); err != nil {
		t.Fatal(err)
	}
	deleted := false
	var deleteErr error
	connection, err := store.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("sse_delete_probe", func(id string) int {
			if id == "during" && !deleted {
				deleted = true
				_, deleteErr = writer.Exec(`DELETE FROM records WHERE request_id='during'`)
			}
			return 1
		}, false)
	})
	connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NormalizeHistoricalSSE(context.Background()); err != nil {
		t.Fatalf("deleted work item blocked startup repair: %v", err)
	}
	if !deleted || deleteErr != nil {
		t.Fatalf("concurrent deletion did not commit: attempted=%v err=%v", deleted, deleteErr)
	}
	var normalized, records int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM normalized_response_requests`).Scan(&normalized); err != nil || normalized != 1 {
		t.Fatalf("deleted sources were marked normalized: count=%d err=%v", normalized, err)
	}
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&records); err != nil || records != 1 {
		t.Fatalf("repair resurrected deleted records: count=%d err=%v", records, err)
	}
	if err := store.NormalizeHistoricalSSE(context.Background()); err != nil {
		t.Fatalf("replayed repair failed: %v", err)
	}
}

// The view function commits a competing writer after the repair's first read
// has acquired a WAL snapshot. A deferred read-to-write upgrade then fails with
// SQLITE_BUSY_SNAPSHOT even though busy_timeout is configured. A writer-first
// transaction prevents that upgrade race and finishes the repair.
func TestHistoricalSSENormalizationReservesWriterBeforeReadingPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.sqlite")
	store, err := OpenStore(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	store.DB.SetMaxOpenConns(1)
	if err := store.PutBatch([]Record{{RequestID: "sse", SessionID: "s", Response: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`CREATE TABLE sse_writer_probe(value INTEGER); INSERT INTO sse_writer_probe VALUES(0); ALTER TABLE blobs RENAME TO sse_blob_data; CREATE VIEW blobs AS SELECT hash,media_type,raw_size,codec,CASE WHEN sse_probe()=1 THEN data ELSE data END AS data FROM sse_blob_data;`); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite3", path+"?_busy_timeout=0&_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.Ping(); err != nil {
		t.Fatal(err)
	}
	attempted := false
	var writerError error
	connection, err := store.DB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(driver any) error {
		return driver.(*sqlite3.SQLiteConn).RegisterFunc("sse_probe", func() int {
			attempted = true
			_, writerError = writer.Exec(`UPDATE sse_writer_probe SET value=value+1`)
			return 1
		}, false)
	})
	connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NormalizeHistoricalSSE(context.Background()); err != nil {
		t.Fatalf("repair failed during live WAL write: %v", err)
	}
	if !attempted {
		t.Fatal("concurrent writer probe did not run")
	}
	var sqliteError sqlite3.Error
	if !errors.As(writerError, &sqliteError) || sqliteError.Code != sqlite3.ErrBusy {
		t.Fatalf("repair did not reserve writer before payload read: %v", writerError)
	}
	var normalized int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM normalized_response_requests WHERE request_id='sse'`).Scan(&normalized); err != nil || normalized != 1 {
		t.Fatalf("normalization marker=%d error=%v", normalized, err)
	}
}
