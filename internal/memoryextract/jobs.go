package memoryextract

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

const jobsApplicationID = 0x43504558

var ErrJobsUnavailable = errors.New("extraction ledger unavailable, unsafe or incompatible")

type Limits struct {
	MaxInputTokens    int `json:"max_input_tokens"`
	MaxOutputTokens   int `json:"max_output_tokens"`
	DailyCalls        int `json:"daily_calls"`
	DailyInputTokens  int `json:"daily_input_tokens"`
	DailyOutputTokens int `json:"daily_output_tokens"`
	MaxAttempts       int `json:"max_attempts"`
}

func DefaultLimits() Limits { return Limits{8000, 1000, 20, 100000, 10000, 3} }

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	fields := []*int{&value.MaxInputTokens, &value.MaxOutputTokens, &value.DailyCalls, &value.DailyInputTokens, &value.DailyOutputTokens, &value.MaxAttempts}
	wants := []int{defaults.MaxInputTokens, defaults.MaxOutputTokens, defaults.DailyCalls, defaults.DailyInputTokens, defaults.DailyOutputTokens, defaults.MaxAttempts}
	for i, field := range fields {
		if *field == 0 {
			*field = wants[i]
		}
		if *field < 1 {
			return value, ErrInvalid
		}
	}
	if value.MaxInputTokens > 8000 || value.MaxOutputTokens > 1000 || value.MaxAttempts > 3 {
		return value, ErrInvalid
	}
	return value, nil
}

type Job struct {
	ID             string    `json:"id"`
	Key            string    `json:"key"`
	State          string    `json:"state"`
	Provider       string    `json:"provider"`
	Attempt        string    `json:"attempt,omitempty"`
	Attempts       int       `json:"attempts"`
	NextAttempt    time.Time `json:"next_attempt,omitempty"`
	ReservedInput  int       `json:"reserved_input"`
	ReservedOutput int       `json:"reserved_output"`
	Input          Input     `json:"input"`
	Report         *Report   `json:"report,omitempty"`
}

// Budget totals are conservative reservations, not a billing receipt. Unknown
// usage and explicitly retried uncertain calls never refund prior reservations.
type JobStatus struct {
	Paused                   bool           `json:"paused"`
	Day                      string         `json:"day"`
	TodayReservations        int            `json:"today_reservations"`
	TodayInput               int            `json:"today_input"`
	TodayOutput              int            `json:"today_output"`
	TodayReportedInput       int            `json:"today_reported_input"`
	TodayReportedOutput      int            `json:"today_reported_output"`
	UsageExceededReservation bool           `json:"usage_exceeded_reservation"`
	States                   map[string]int `json:"states"`
	Limits                   Limits         `json:"limits"`
}

type Jobs struct {
	db         *sql.DB
	workerFile *os.File
	limits     Limits
	now        func() time.Time
	workerMu   sync.Mutex
	held       bool
	closed     bool
}

func privateRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return ErrJobsUnavailable
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return ErrJobsUnavailable
	}
	return nil
}

// Inspect existing bytes read-only before setting WAL, migrating schema or
// creating a lock. An archive, projection, outbox or unrelated SQLite DB stays
// untouched when accidentally supplied as the task ledger path.
func inspectJobs(path string, empty bool) error {
	location := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", location.String()+"?mode=ro&_query_only=on&_busy_timeout=5000")
	if err != nil {
		return ErrJobsUnavailable
	}
	defer db.Close()
	var application, version, tables int
	if db.QueryRow(`PRAGMA application_id`).Scan(&application) != nil || db.QueryRow(`PRAGMA user_version`).Scan(&version) != nil {
		return ErrJobsUnavailable
	}
	if empty && application == 0 && version == 0 {
		return nil
	}
	if application != jobsApplicationID || version != 1 || db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('extract_jobs','extract_budget','extract_state','extract_cursors')`).Scan(&tables) != nil || tables != 4 {
		return ErrJobsUnavailable
	}
	var settings []byte
	if db.QueryRow(`SELECT limits FROM extract_state WHERE id=1`).Scan(&settings) != nil {
		return ErrJobsUnavailable
	}
	return nil
}

func OpenJobs(path string, limits Limits) (*Jobs, error) {
	defaultSettings := limits == (Limits{})
	limits, err := normalizeLimits(limits)
	if err != nil || !filepath.IsAbs(path) {
		return nil, ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, ErrJobsUnavailable
	}
	dir, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		return nil, ErrJobsUnavailable
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrJobsUnavailable
	}
	file := os.NewFile(uintptr(fd), path)
	info, statErr := file.Stat()
	_ = file.Close()
	if statErr != nil || privateRegular(path) != nil || inspectJobs(path, info.Size() == 0) != nil {
		return nil, ErrJobsUnavailable
	}
	lockFD, err := unix.Open(path+".worker.lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrJobsUnavailable
	}
	lock := os.NewFile(uintptr(lockFD), path+".worker.lock")
	if privateRegular(path+".worker.lock") != nil {
		lock.Close()
		return nil, ErrJobsUnavailable
	}
	location := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", location.String()+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_txlock=immediate&_foreign_keys=on")
	if err != nil {
		lock.Close()
		return nil, ErrJobsUnavailable
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Jobs{db: db, workerFile: lock, limits: limits, now: time.Now}
	fail := func() (*Jobs, error) { s.Close(); return nil, ErrJobsUnavailable }
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS extract_state(id INTEGER PRIMARY KEY CHECK(id=1),paused INTEGER NOT NULL DEFAULT 0,overrun INTEGER NOT NULL DEFAULT 0,limits BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS extract_jobs(id TEXT PRIMARY KEY,input BLOB NOT NULL,provider TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('pending','reserved','in_flight','generated','done','retry','failed','uncertain')),attempt TEXT NOT NULL DEFAULT '',attempts INTEGER NOT NULL DEFAULT 0,next_ns INTEGER NOT NULL DEFAULT 0,reserved_input INTEGER NOT NULL DEFAULT 0,reserved_output INTEGER NOT NULL DEFAULT 0,reservation_day TEXT NOT NULL DEFAULT '',report BLOB,created_ns INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS extract_pending ON extract_jobs(state,next_ns,created_ns,id);
 CREATE TABLE IF NOT EXISTS extract_budget(day TEXT PRIMARY KEY,calls INTEGER NOT NULL DEFAULT 0,input INTEGER NOT NULL DEFAULT 0,output INTEGER NOT NULL DEFAULT 0,reported_input INTEGER NOT NULL DEFAULT 0,reported_output INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS extract_cursors(scan_key TEXT PRIMARY KEY,cursor BLOB NOT NULL);
 PRAGMA application_id=1129334104; PRAGMA user_version=1;`); err != nil {
		return fail()
	}
	settings, _ := json.Marshal(limits)
	if _, err := db.Exec(`INSERT OR IGNORE INTO extract_state(id,limits) VALUES(1,?)`, settings); err != nil {
		return fail()
	}
	var storedRaw []byte
	var stored Limits
	if db.QueryRow(`SELECT limits FROM extract_state WHERE id=1`).Scan(&storedRaw) != nil || json.Unmarshal(storedRaw, &stored) != nil {
		return fail()
	}
	checked, err := normalizeLimits(stored)
	if err != nil || checked != stored {
		return fail()
	}
	if !defaultSettings && limits != stored {
		s.Close()
		return nil, ErrInvalid
	}
	s.limits = stored
	if s.tryWorker() == nil {
		err = s.recover(context.Background())
		s.releaseWorker()
		if err != nil {
			return fail()
		}
	}
	return s, nil
}

func (s *Jobs) tryWorker() error {
	if s.closed || s.held || unix.Flock(int(s.workerFile.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return ErrBusy
	}
	s.held = true
	return nil
}

func (s *Jobs) releaseWorker() {
	if s.held {
		_ = unix.Flock(int(s.workerFile.Fd()), unix.LOCK_UN)
		s.held = false
	}
}

func (s *Jobs) Close() error {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.db.Close()
	s.releaseWorker()
	_ = s.workerFile.Close()
	return err
}

// Worker-lock possession, never an elapsed lease, proves another ledger worker
// cannot still authorize calls. Dispatch ambiguity remains uncertain regardless.
func (s *Jobs) recover(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE extract_jobs SET state=CASE WHEN state='in_flight' THEN 'uncertain' WHEN attempts>=? THEN 'failed' ELSE 'pending' END,attempt=CASE WHEN state='in_flight' THEN attempt ELSE '' END,next_ns=0 WHERE state IN ('reserved','in_flight')`, s.limits.MaxAttempts)
	return err
}

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var input, report []byte
	var next int64
	err := row.Scan(&job.ID, &input, &job.Provider, &job.State, &job.Attempt, &job.Attempts, &next, &job.ReservedInput, &job.ReservedOutput, &report)
	if errors.Is(err, sql.ErrNoRows) {
		return job, ErrInvalid
	}
	if err != nil || json.Unmarshal(input, &job.Input) != nil {
		return Job{}, ErrJobsUnavailable
	}
	job.Key = job.ID
	if next > 0 {
		job.NextAttempt = time.Unix(0, next).UTC()
	}
	if len(report) > 0 {
		job.Report = new(Report)
		if json.Unmarshal(report, job.Report) != nil {
			return Job{}, ErrJobsUnavailable
		}
	}
	return job, nil
}

const jobColumns = `id,input,provider,state,attempt,attempts,next_ns,reserved_input,reserved_output,report`

func (s *Jobs) Get(ctx context.Context, id string) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs WHERE id=?`, id))
}

func enqueueTx(ctx context.Context, tx *sql.Tx, key string, input Input, provider string, now time.Time) (Job, error) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 1<<20 || key == "" || key != Key(input, provider) || provider == "" || len(provider) > 2048 || validateShape(input) != nil {
		return Job{}, ErrInvalid
	}
	var oldInput []byte
	var oldProvider string
	err = tx.QueryRowContext(ctx, `SELECT input,provider FROM extract_jobs WHERE id=?`, key).Scan(&oldInput, &oldProvider)
	if err == nil {
		if string(raw) != string(oldInput) || provider != oldProvider {
			return Job{}, ErrInvalid
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO extract_jobs(id,input,provider,state,created_ns) VALUES(?,?,?,'pending',?)`, key, raw, provider, now.UnixNano()); err != nil {
			return Job{}, ErrJobsUnavailable
		}
	} else {
		return Job{}, ErrJobsUnavailable
	}
	return scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs WHERE id=?`, key))
}

func (s *Jobs) Enqueue(ctx context.Context, key string, input Input, provider string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	job, err := enqueueTx(ctx, tx, key, input, provider, s.now())
	if err == nil {
		err = tx.Commit()
	}
	return job, err
}

func (s *Jobs) Reserve(ctx context.Context, id string, inputTokens, outputTokens int) (Job, error) {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if inputTokens < 1 || inputTokens > s.limits.MaxInputTokens || outputTokens < 1 || outputTokens > s.limits.MaxOutputTokens {
		return Job{}, ErrBudget
	}
	if err := s.tryWorker(); err != nil {
		return Job{}, err
	}
	keep := false
	defer func() {
		if !keep {
			s.releaseWorker()
		}
	}()
	if err := s.recover(ctx); err != nil {
		return Job{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	var paused bool
	if err := tx.QueryRowContext(ctx, `SELECT paused FROM extract_state WHERE id=1`).Scan(&paused); err != nil {
		return Job{}, err
	}
	if paused {
		return Job{}, ErrPaused
	}
	var uncertain bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM extract_jobs WHERE state='uncertain')`).Scan(&uncertain); err != nil {
		return Job{}, err
	}
	if uncertain {
		return Job{}, ErrUncertain
	}
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs WHERE id=?`, id))
	if err != nil {
		return Job{}, err
	}
	now := s.now().UTC()
	if (job.State != "pending" && job.State != "retry") || job.NextAttempt.After(now) {
		return Job{}, ErrBusy
	}
	if job.Attempts >= s.limits.MaxAttempts {
		return Job{}, ErrBudget
	}
	day := now.Format("2006-01-02")
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO extract_budget(day) VALUES(?)`, day); err != nil {
		return Job{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE extract_budget SET calls=calls+1,input=input+?,output=output+? WHERE day=? AND calls<? AND input<=? AND output<=?`, inputTokens, outputTokens, day, s.limits.DailyCalls, s.limits.DailyInputTokens-inputTokens, s.limits.DailyOutputTokens-outputTokens)
	if err != nil {
		return Job{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Job{}, ErrBudget
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return Job{}, ErrJobsUnavailable
	}
	job.Attempt, job.Attempts, job.State = hex.EncodeToString(token), job.Attempts+1, "reserved"
	job.ReservedInput, job.ReservedOutput, job.NextAttempt = inputTokens, outputTokens, time.Time{}
	if _, err := tx.ExecContext(ctx, `UPDATE extract_jobs SET state='reserved',attempt=?,attempts=?,next_ns=0,reserved_input=?,reserved_output=?,reservation_day=? WHERE id=?`, job.Attempt, job.Attempts, inputTokens, outputTokens, day, id); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	keep = true
	return job, nil
}

func (s *Jobs) Start(ctx context.Context, id, attempt string) error {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if !s.held || s.closed || attempt == "" {
		return ErrBusy
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var paused bool
	if err := tx.QueryRowContext(ctx, `SELECT paused FROM extract_state WHERE id=1`).Scan(&paused); err != nil {
		return err
	}
	if paused {
		return ErrPaused
	}
	result, err := tx.ExecContext(ctx, `UPDATE extract_jobs SET state='in_flight' WHERE id=? AND attempt=? AND state='reserved'`, id, attempt)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrBusy
	}
	return tx.Commit()
}

func (s *Jobs) SaveResult(ctx context.Context, id, attempt string, report Report) error {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	raw, err := json.Marshal(report)
	if err != nil || len(raw) > 1<<20 || report.JobID != id || report.Version != Version || !report.RequiresHumanReview || report.Usage.InputTokens < 0 || report.Usage.OutputTokens < 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs WHERE id=?`, id))
	if err != nil {
		return err
	}
	storedRaw, _ := json.Marshal(job.Report)
	if job.Attempt == attempt && attempt != "" && (job.State == "generated" || job.State == "done") && bytes.Equal(storedRaw, raw) {
		return nil
	}
	if !s.held || s.closed || attempt == "" || job.Attempt != attempt || job.State != "in_flight" || report.ProjectID != job.Input.ProjectID || report.SessionID != job.Input.SessionID {
		return ErrBusy
	}
	if _, err := tx.ExecContext(ctx, `UPDATE extract_jobs SET state='generated',report=? WHERE id=? AND attempt=?`, raw, id, attempt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE extract_budget SET reported_input=reported_input+?,reported_output=reported_output+? WHERE day=(SELECT reservation_day FROM extract_jobs WHERE id=?)`, report.Usage.InputTokens, report.Usage.OutputTokens, id); err != nil {
		return err
	}
	if report.Usage.InputTokens > job.ReservedInput || report.Usage.OutputTokens > job.ReservedOutput {
		if _, err := tx.ExecContext(ctx, `UPDATE extract_state SET paused=1,overrun=1 WHERE id=1`); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.releaseWorker()
	return nil
}

func (s *Jobs) Fail(ctx context.Context, id, attempt string, retryable, uncertain bool) error {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if !s.held || s.closed || attempt == "" {
		return ErrBusy
	}
	job, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.Attempt != attempt || (job.State != "reserved" && job.State != "in_flight") {
		return ErrBusy
	}
	state, next := "failed", int64(0)
	if uncertain {
		state = "uncertain"
	} else if retryable && job.Attempts < s.limits.MaxAttempts {
		state = "retry"
		next = s.now().Add(time.Duration(1<<job.Attempts) * time.Second).UnixNano()
	}
	result, err := s.db.ExecContext(ctx, `UPDATE extract_jobs SET state=?,next_ns=? WHERE id=? AND attempt=? AND state IN ('reserved','in_flight')`, state, next, id, attempt)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrBusy
	}
	s.releaseWorker()
	return nil
}

func (s *Jobs) Complete(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE extract_jobs SET state='done' WHERE id=? AND state IN ('generated','done')`, id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrInvalid
	}
	return nil
}

// Invalidated queued evidence is terminal without spending a provider budget.
// Active or already-generated jobs require their own fenced transition.
func (s *Jobs) Invalidate(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE extract_jobs SET state='failed',next_ns=0 WHERE id=? AND state IN ('pending','retry')`, id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrBusy
	}
	return nil
}

// Only an explicit operator action can release an uncertain outcome. Retrying
// acknowledges possible duplicate execution/cost and consumes a new reservation.
func (s *Jobs) ResolveUncertain(ctx context.Context, id, expectedAttempt, action string, confirmed bool) error {
	if !confirmed || expectedAttempt == "" || (action != "abandon" && action != "retry") {
		return ErrInvalid
	}
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if err := s.tryWorker(); err != nil {
		return err
	}
	defer s.releaseWorker()
	job, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.State != "uncertain" || job.Attempt != expectedAttempt {
		return ErrUncertain
	}
	state := "failed"
	if action == "retry" {
		if job.Attempts >= s.limits.MaxAttempts {
			return ErrBudget
		}
		state = "retry"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE extract_jobs SET state=?,next_ns=0 WHERE id=? AND state='uncertain' AND attempt=?`, state, id, expectedAttempt)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrUncertain
	}
	return nil
}

func (s *Jobs) ListPending(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs WHERE state IN ('pending','generated') OR (state='retry' AND next_ns<=?) ORDER BY (state='generated') DESC,created_ns,id LIMIT ?`, s.now().UnixNano(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// Filter before LIMIT so another project or provider profile cannot starve the
// selected worker. The ledger stores exact input JSON as its private payload.
func (s *Jobs) ListPendingFor(ctx context.Context, selection Selection, provider string, limit int) ([]Job, error) {
	if !validSelection(selection) || provider == "" || len(provider) > 2048 || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs WHERE
 (state IN ('pending','generated') OR (state='retry' AND next_ns<=?)) AND provider=?
 AND json_extract(CAST(input AS TEXT),'$.project_id')=?
 AND (?='' OR json_extract(CAST(input AS TEXT),'$.session_id')=?)
 ORDER BY (state='generated') DESC,created_ns,id LIMIT ?`, s.now().UnixNano(), provider, selection.ProjectID, selection.SessionID, selection.SessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// List is a private control-plane view. Callers exposing status must select
// metadata explicitly rather than serializing these source-bearing objects.
func (s *Jobs) List(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM extract_jobs ORDER BY created_ns DESC,id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Jobs) Pause(ctx context.Context, paused bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE extract_state SET paused=? WHERE id=1`, paused)
	return err
}

func (s *Jobs) Status(ctx context.Context) (JobStatus, error) {
	out := JobStatus{Day: s.now().UTC().Format("2006-01-02"), States: map[string]int{}, Limits: s.limits}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT paused,overrun FROM extract_state WHERE id=1`).Scan(&out.Paused, &out.UsageExceededReservation); err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, `SELECT calls,input,output,reported_input,reported_output FROM extract_budget WHERE day=?`, out.Day).Scan(&out.TodayReservations, &out.TodayInput, &out.TodayOutput, &out.TodayReportedInput, &out.TodayReportedOutput)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT state,COUNT(*) FROM extract_jobs GROUP BY state`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return out, err
		}
		out.States[state] = count
	}
	err = rows.Err()
	rows.Close()
	return out, err
}

func scanKey(selection Selection, provider string) (string, error) {
	if !validSelection(selection) || provider == "" || len(provider) > 2048 {
		return "", ErrInvalid
	}
	raw, _ := json.Marshal(struct {
		Selection         Selection
		Provider, Version string
	}{selection, provider, Version})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func cursorTx(ctx context.Context, tx *sql.Tx, key string) (ScanCursor, error) {
	var cursor ScanCursor
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT cursor FROM extract_cursors WHERE scan_key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return cursor, nil
	}
	if err != nil || json.Unmarshal(raw, &cursor) != nil {
		return cursor, ErrJobsUnavailable
	}
	return cursor, nil
}

func (s *Jobs) Cursor(ctx context.Context, selection Selection, provider string) (ScanCursor, error) {
	key, err := scanKey(selection, provider)
	if err != nil {
		return ScanCursor{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScanCursor{}, err
	}
	defer tx.Rollback()
	return cursorTx(ctx, tx, key)
}

func (s *Jobs) EnqueueAndAdvance(ctx context.Context, selection Selection, provider string, expected, next ScanCursor, inputs []Input) ([]Job, error) {
	key, err := scanKey(selection, provider)
	if err != nil || !validArchiveID(next.ArchiveID) || next.Watermark < 0 || next.ChangeAfter < 0 || next.ChangeAfter > next.Watermark || next.BackfillAfterID < 0 || (expected.ArchiveID != "" && expected.ArchiveID != next.ArchiveID) || next.Watermark < expected.Watermark || next.ChangeAfter < expected.ChangeAfter || next.BackfillAfterID < expected.BackfillAfterID || expected.BackfillDone && !next.BackfillDone || expected.Incomplete && !next.Incomplete || len(inputs) > MaxScanInputs {
		return nil, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := cursorTx(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, ErrBusy
	}
	jobs := make([]Job, 0, len(inputs))
	for _, input := range inputs {
		if input.ArchiveID != next.ArchiveID || input.ProjectID != selection.ProjectID || selection.SessionID != "" && input.SessionID != selection.SessionID || input.Sequence > next.Watermark {
			return nil, ErrInvalid
		}
		job, err := enqueueTx(ctx, tx, Key(input, provider), input, provider, s.now())
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	raw, _ := json.Marshal(next)
	if _, err := tx.ExecContext(ctx, `INSERT INTO extract_cursors(scan_key,cursor) VALUES(?,?) ON CONFLICT(scan_key) DO UPDATE SET cursor=excluded.cursor`, key, raw); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return jobs, nil
}
