// Package outbox stores exact delivery bytes until a matching archive receipt.
// It is a bounded transport buffer, never a source of reviewed memory.
package outbox

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"cpa-session-archive/delivery"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

var (
	ErrFull     = errors.New("outbox capacity exhausted")
	ErrOversize = errors.New("delivery exceeds outbox byte budget")
	ErrEmpty    = errors.New("outbox has no pending delivery")
	ErrCorrupt  = errors.New("outbox payload digest mismatch")
)

type Store struct {
	db         *sql.DB
	maxBytes   int64
	maxEntries int
}
type Entry struct {
	Digest string
	Body   []byte
}
type Status = delivery.OutboxStatus

func Open(path string, maxBytes int64, maxEntries int) (*Store, error) {
	if !filepath.IsAbs(path) || maxBytes <= 0 || maxEntries <= 0 {
		return nil, errors.New("invalid outbox configuration")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("outbox directory must be private")
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("outbox file must be private and regular")
	}
	location := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", location.String()+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_secure_delete=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fail(err)
	}
	if version > 1 {
		return fail(errors.New("unsupported outbox schema"))
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS deliveries(sequence INTEGER PRIMARY KEY AUTOINCREMENT,digest TEXT NOT NULL UNIQUE,body BLOB NOT NULL,state INTEGER NOT NULL DEFAULT 0,rejection INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS capacity(id INTEGER PRIMARY KEY CHECK(id=1),entries INTEGER NOT NULL,bytes INTEGER NOT NULL);
 INSERT OR IGNORE INTO capacity VALUES(1,0,0);
 CREATE TRIGGER IF NOT EXISTS outbox_insert AFTER INSERT ON deliveries BEGIN UPDATE capacity SET entries=entries+1,bytes=bytes+length(NEW.body) WHERE id=1; END;
 CREATE TRIGGER IF NOT EXISTS outbox_delete AFTER DELETE ON deliveries BEGIN UPDATE capacity SET entries=entries-1,bytes=bytes-length(OLD.body) WHERE id=1; END;
 PRAGMA user_version=1;`)
	if err != nil {
		return fail(err)
	}
	return &Store{db: db, maxBytes: maxBytes, maxEntries: maxEntries}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Put commits before returning. Identical bytes share one outbox entry, including
// quarantined entries. Capacity includes quarantine; lowering limits evicts none.
func (s *Store) Put(ctx context.Context, body []byte) (string, bool, error) {
	hash := sha256.Sum256(body)
	digest := hex.EncodeToString(hash[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM deliveries WHERE digest=?`, digest).Scan(&exists)
	if err == nil {
		return digest, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	if int64(len(body)) > s.maxBytes {
		return "", false, ErrOversize
	}
	var entries int
	var used int64
	if err = tx.QueryRowContext(ctx, `SELECT entries,bytes FROM capacity WHERE id=1`).Scan(&entries, &used); err != nil {
		return "", false, err
	}
	if entries >= s.maxEntries || int64(len(body)) > s.maxBytes-used {
		return "", false, ErrFull
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO deliveries(digest,body) VALUES(?,?)`, digest, body); err != nil {
		return "", false, err
	}
	if err = tx.Commit(); err != nil {
		return "", false, err
	}
	return digest, true, nil
}

func (s *Store) Next(ctx context.Context) (Entry, error) {
	var entry Entry
	err := s.db.QueryRowContext(ctx, `SELECT digest,body FROM deliveries WHERE state=0 ORDER BY sequence LIMIT 1`).Scan(&entry.Digest, &entry.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrEmpty
	}
	if err != nil {
		return Entry{}, err
	}
	hash := sha256.Sum256(entry.Body)
	if hex.EncodeToString(hash[:]) != entry.Digest {
		return Entry{}, ErrCorrupt
	}
	return entry, nil
}

// Acknowledge is called only after the transport verifies the commit protocol
// and digest. Removing an absent entry is safe after an uncertain prior delete.
func (s *Store) Acknowledge(ctx context.Context, digest string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deliveries WHERE digest=? AND state=0`, digest)
	return err
}

// Quarantine retains rejected bytes without blocking later pending deliveries.
func (s *Store) Quarantine(ctx context.Context, digest string, status int) error {
	if status != 400 && status != 413 && status != 422 {
		return errors.New("not a permanent delivery rejection")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deliveries SET state=1,rejection=? WHERE digest=?`, status, digest)
	return err
}

func (s *Store) Status(ctx context.Context) (Status, error) {
	out := Status{MaxBytes: s.maxBytes, MaxEntries: s.maxEntries}
	err := s.db.QueryRowContext(ctx, `SELECT entries,bytes,(SELECT COUNT(*) FROM deliveries WHERE state=1) FROM capacity WHERE id=1`).Scan(&out.Entries, &out.Bytes, &out.Quarantined)
	out.Pending = out.Entries - out.Quarantined
	return out, err
}
