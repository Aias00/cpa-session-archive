package memoryextract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/storage"
)

const (
	MaxBundleBytes    = 32 << 20
	MaxWindowBytes    = 2600
	MaxSourceBytes    = 1 << 20
	MaxSourceWindows  = 256
	MaxSourceExcerpts = 2048
	MaxScanBytes      = 4 << 20
	MaxScanInputs     = 1024
	MaxScanSources    = 256
	MaxScanPages      = 32
)

func sourceSize(snapshot archive.SourceSnapshot) error {
	r := snapshot.Record
	if int64(len(r.OriginalRequest))+int64(len(r.Response))+int64(len(r.UpstreamRequest)) > MaxSourceBytes {
		return fmt.Errorf("%w: retained source payload exceeds 1 MiB", ErrBudget)
	}
	return nil
}

func validSelection(s Selection) bool {
	return s.ProjectID != "" && strings.TrimSpace(s.ProjectID) == s.ProjectID && len(s.ProjectID) <= 1024 && strings.TrimSpace(s.SessionID) == s.SessionID && len(s.SessionID) <= 1024
}

func validArchiveID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && len(id) <= 256
}

func recordScope(r archive.Record) (project, client string) {
	first := func(keys ...string) string {
		for _, key := range keys {
			if values := r.Facets[key]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
		return ""
	}
	project = first("project.id")
	if project == "" {
		if remote := first("git.remote"); remote != "" {
			project = "git:" + remote
		}
	}
	return project, first("client", "client.name")
}

func inSelection(r archive.Record, s Selection) bool {
	project, _ := recordScope(r)
	return project == s.ProjectID && (s.SessionID == "" || s.SessionID == r.SessionID)
}

func sourceRevision(r archive.Record) (string, error) {
	r.PrincipalAlias = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("%w: source cannot be encoded", ErrInvalid)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func validSnapshot(snapshot archive.SourceSnapshot) bool {
	if snapshot.Record.RequestID == "" || len(snapshot.Record.RequestID) > 1024 || snapshot.Sequence < 0 {
		return false
	}
	revision, err := sourceRevision(snapshot.Record)
	return err == nil && revision == snapshot.Revision
}

func eligibleMessage(m memory.Message) bool {
	return (m.Kind == "text" && (m.Role == "user" || m.Role == "assistant")) || (m.Kind == "tool_result" && m.Role == "tool")
}

// InputsFromSnapshot builds small, literal windows. Speaker attribution is
// copied from the normalizer and cannot be supplied by an extraction model.
func InputsFromSnapshot(snapshot archive.SourceSnapshot, archiveID string, selection Selection) ([]Input, error) {
	if err := sourceSize(snapshot); err != nil {
		return nil, err
	}
	if !validSelection(selection) || !validArchiveID(archiveID) || !validSnapshot(snapshot) {
		return nil, fmt.Errorf("%w: source identity, revision or scope", ErrInvalid)
	}
	if !inSelection(snapshot.Record, selection) || memory.IsInternalSource(snapshot.Record) {
		return []Input{}, nil
	}
	doc := memory.Normalize(snapshot.Record)
	project, _ := recordScope(snapshot.Record)
	base := Input{Version: Version, ArchiveID: archiveID, ProjectID: project, SessionID: snapshot.Record.SessionID, RequestID: snapshot.Record.RequestID, Revision: snapshot.Revision, Sequence: snapshot.Sequence}
	windows := []Input{}
	current := base
	excerptCount := 0
	flush := func() error {
		if len(current.Excerpts) > 0 {
			if len(windows) >= MaxSourceWindows {
				return fmt.Errorf("%w: source exceeds window limit", ErrBudget)
			}
			windows = append(windows, current)
			current = base
			current.Window = len(windows)
		}
		return nil
	}
	for index, message := range doc.Messages {
		if !eligibleMessage(message) {
			continue
		}
		runes := []rune(message.Text)
		for start := 0; start < len(runes); {
			limit := min(1600, len(runes)-start)
			excerpt := func(n int) Excerpt {
				return Excerpt{ID: fmt.Sprintf("m%d:%d:%d", index, start, start+n), Role: message.Role, Kind: message.Kind, ToolName: message.ToolName, ToolCallID: message.ToolCallID, Text: string(runes[start : start+n]), Partial: doc.Partial, Citation: memory.Citation{NormalizerVersion: memory.NormalizerVersion, RequestID: snapshot.Record.RequestID, Revision: snapshot.Revision, Side: message.Side, Pointer: message.Pointer, Start: start, End: start + n}}
			}
			fits := func(n int) bool {
				candidate := current
				candidate.Excerpts = append(append([]Excerpt(nil), current.Excerpts...), excerpt(n))
				raw, err := json.Marshal(candidate)
				return err == nil && len(raw) <= MaxWindowBytes
			}
			low, high := 0, limit
			for low < high {
				mid := (low + high + 1) / 2
				if fits(mid) {
					low = mid
				} else {
					high = mid - 1
				}
			}
			if low == 0 {
				if len(current.Excerpts) == 0 {
					return nil, fmt.Errorf("%w: source metadata exceeds window budget", ErrInvalid)
				}
				if err := flush(); err != nil {
					return nil, err
				}
				continue
			}
			// Normalization can retain large blank runs around useful text.
			// Blank-only spans cannot support candidates, and the ledger rejects
			// them. Preserve original offsets while advancing past those spans.
			if strings.TrimSpace(string(runes[start:start+low])) == "" {
				start += low
				continue
			}
			excerptCount++
			if excerptCount > MaxSourceExcerpts {
				return nil, fmt.Errorf("%w: source exceeds excerpt limit", ErrBudget)
			}
			current.Excerpts = append(current.Excerpts, excerpt(low))
			start += low
			if start < len(runes) {
				if err := flush(); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return windows, nil
}

// SourceReader validates retained evidence without creating a projection DB.
// Search and recap are intentionally unavailable on this source-only adapter.
type SourceReader struct {
	feed      storage.MemoryFeed
	archiveID string
	selection Selection
}

var _ memory.Reader = (*SourceReader)(nil)

func NewSourceReader(feed storage.MemoryFeed, archiveID string, selection Selection) (*SourceReader, error) {
	if feed == nil || !validSelection(selection) || !validArchiveID(archiveID) {
		return nil, fmt.Errorf("%w: a feed, archive identity and fixed project scope are required", ErrInvalid)
	}
	return &SourceReader{feed: feed, archiveID: archiveID, selection: selection}, nil
}

func (r *SourceReader) checkIdentity(ctx context.Context) error {
	if feed, ok := r.feed.(storage.IdentityFeed); ok {
		identity, err := feed.MemoryIdentity(ctx)
		if err != nil {
			return err
		}
		if identity.ArchiveID != r.archiveID || identity.Watermark < 0 {
			return memory.ErrSourceUnavailable
		}
	}
	return nil
}

func (r *SourceReader) source(ctx context.Context, id string) (archive.SourceSnapshot, error) {
	if err := r.checkIdentity(ctx); err != nil {
		return archive.SourceSnapshot{}, err
	}
	source, err := r.feed.MemorySource(ctx, id)
	if errors.Is(err, storage.ErrSourceNotFound) {
		return archive.SourceSnapshot{}, memory.ErrSourceUnavailable
	}
	if err != nil {
		return archive.SourceSnapshot{}, err
	}
	if err := sourceSize(source); err != nil {
		return archive.SourceSnapshot{}, err
	}
	if source.Record.RequestID != id || !validSnapshot(source) || !inSelection(source.Record, r.selection) || memory.IsInternalSource(source.Record) {
		return archive.SourceSnapshot{}, memory.ErrSourceUnavailable
	}
	return source, r.checkIdentity(ctx)
}

func (r *SourceReader) ReadSource(ctx context.Context, citation memory.Citation) (memory.Evidence, error) {
	out := memory.Evidence{Citation: citation}
	if citation.NormalizerVersion != memory.NormalizerVersion || citation.RequestID == "" || citation.Revision == "" || citation.Start < 0 || citation.End <= citation.Start || citation.End-citation.Start > 1600 {
		return out, memory.ErrSourceUnavailable
	}
	snapshot, err := r.source(ctx, citation.RequestID)
	if err != nil {
		return out, err
	}
	if snapshot.Revision != citation.Revision {
		return out, memory.ErrSourceUnavailable
	}
	doc := memory.Normalize(snapshot.Record)
	for _, message := range doc.Messages {
		if message.Side != citation.Side || message.Pointer != citation.Pointer {
			continue
		}
		runes := []rune(message.Text)
		if citation.End > len(runes) {
			return out, memory.ErrSourceUnavailable
		}
		out.ProjectID, out.Client = recordScope(snapshot.Record)
		out.SessionID = snapshot.Record.SessionID
		out.Text = string(runes[citation.Start:citation.End])
		out.Kind, out.ToolName, out.ToolCallID = message.Kind, message.ToolName, message.ToolCallID
		out.Model, out.MessageAt = message.Model, message.MessageAt
		out.Partial, out.Issues = doc.Partial, doc.Issues
		return out, nil
	}
	return out, memory.ErrSourceUnavailable
}

// VerifyInput reconstructs the trusted input fields. Existing reviewed entries
// are supplied separately by the knowledge reader and are not source content.
func (r *SourceReader) VerifyInput(ctx context.Context, input Input) error {
	if input.RequestID == "" || input.Window < 0 {
		return fmt.Errorf("%w: invalid source window", ErrInvalid)
	}
	snapshot, err := r.source(ctx, input.RequestID)
	if err != nil {
		return err
	}
	windows, err := InputsFromSnapshot(snapshot, r.archiveID, r.selection)
	if err != nil {
		return err
	}
	if input.Window < 0 || input.Window >= len(windows) {
		return fmt.Errorf("%w: source window is unavailable", memory.ErrSourceUnavailable)
	}
	trusted := windows[input.Window]
	input.Existing, trusted.Existing = nil, nil
	input.KnowledgeComplete, trusted.KnowledgeComplete = false, false
	if !reflect.DeepEqual(input, trusted) {
		return fmt.Errorf("%w: source window differs from retained evidence", memory.ErrSourceUnavailable)
	}
	return nil
}

func (*SourceReader) Search(context.Context, memory.Query) (memory.SearchResult, error) {
	return memory.SearchResult{}, memory.ErrInvalidQuery
}
func (*SourceReader) Recap(context.Context, memory.Query) (memory.RecapResult, error) {
	return memory.RecapResult{}, memory.ErrInvalidQuery
}
func (r *SourceReader) Status(ctx context.Context) (memory.Status, error) {
	return memory.Status{ScopeFiltered: true}, r.checkIdentity(ctx)
}

// Bundle is an immutable offline collection of current source snapshots. It is
// not a claim that all historical mutations or deleted records are retained.
type Bundle struct {
	ArchiveID string                   `json:"archive_id"`
	Watermark int64                    `json:"watermark"`
	Sources   []archive.SourceSnapshot `json:"sources"`
}

type BundleFeed struct {
	bundle  Bundle
	byID    map[string]int
	changes []archive.SourceChange
}

var _ storage.MemoryFeed = (*BundleFeed)(nil)
var _ storage.IdentityFeed = (*BundleFeed)(nil)

func DecodeBundle(reader io.Reader) (*BundleFeed, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBundleBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBundleBytes {
		return nil, fmt.Errorf("%w: source bundle exceeds 32 MiB", ErrInvalid)
	}
	var bundle Bundle
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&bundle) != nil {
		return nil, fmt.Errorf("%w: malformed source bundle", ErrInvalid)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("%w: trailing source bundle content", ErrInvalid)
	}
	return NewBundleFeed(bundle)
}

func NewBundleFeed(bundle Bundle) (*BundleFeed, error) {
	if !validArchiveID(bundle.ArchiveID) || bundle.Watermark < 0 {
		return nil, fmt.Errorf("%w: source bundle identity or fence", ErrInvalid)
	}
	raw, err := json.Marshal(bundle)
	if err != nil || len(raw) > MaxBundleBytes {
		return nil, fmt.Errorf("%w: source bundle cannot be encoded within 32 MiB", ErrInvalid)
	}
	// Own all byte slices and maps; caller mutation cannot change a frozen feed.
	var frozen Bundle
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&frozen); err != nil {
		return nil, fmt.Errorf("%w: malformed source bundle", ErrInvalid)
	}
	// A bundle's synthetic backfill row IDs must survive harmless JSON reordering.
	sort.Slice(frozen.Sources, func(i, j int) bool {
		return frozen.Sources[i].Record.RequestID < frozen.Sources[j].Record.RequestID
	})
	feed := &BundleFeed{bundle: frozen, byID: map[string]int{}}
	sequences := map[int64]bool{}
	for index, source := range frozen.Sources {
		if err := sourceSize(source); err != nil {
			return nil, err
		}
		if !validSnapshot(source) || source.Sequence > frozen.Watermark {
			return nil, fmt.Errorf("%w: source bundle revision or sequence", ErrInvalid)
		}
		if _, duplicate := feed.byID[source.Record.RequestID]; duplicate || (source.Sequence > 0 && sequences[source.Sequence]) {
			return nil, fmt.Errorf("%w: duplicate bundle source or sequence", ErrInvalid)
		}
		feed.byID[source.Record.RequestID] = index
		sequences[source.Sequence] = true
		if source.Sequence > 0 {
			feed.changes = append(feed.changes, archive.SourceChange{Sequence: source.Sequence, RequestID: source.Record.RequestID})
		}
	}
	sort.Slice(feed.changes, func(i, j int) bool { return feed.changes[i].Sequence < feed.changes[j].Sequence })
	return feed, nil
}

func (f *BundleFeed) MemoryIdentity(ctx context.Context) (storage.FeedIdentity, error) {
	return storage.FeedIdentity{ArchiveID: f.bundle.ArchiveID, Watermark: f.bundle.Watermark, HistoryComplete: false}, ctx.Err()
}
func (f *BundleFeed) MemoryWatermark(ctx context.Context) (int64, error) {
	return f.bundle.Watermark, ctx.Err()
}
func (f *BundleFeed) MemorySource(ctx context.Context, id string) (archive.SourceSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return archive.SourceSnapshot{}, err
	}
	index, found := f.byID[id]
	if !found {
		return archive.SourceSnapshot{}, storage.ErrSourceNotFound
	}
	raw, _ := json.Marshal(f.bundle.Sources[index])
	var copy archive.SourceSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&copy); err != nil {
		return archive.SourceSnapshot{}, err
	}
	return copy, nil
}
func (f *BundleFeed) MemoryRevision(ctx context.Context, id string) (string, error) {
	source, err := f.MemorySource(ctx, id)
	return source.Revision, err
}
func feedPageSize(limit int) int {
	if limit < 1 || limit > 256 {
		return 64
	}
	return limit
}
func (f *BundleFeed) MemoryBackfill(ctx context.Context, after int64, limit int) (archive.SourcePage, error) {
	if err := ctx.Err(); err != nil {
		return archive.SourcePage{}, err
	}
	if after < 0 || after > int64(len(f.bundle.Sources)) {
		return archive.SourcePage{}, fmt.Errorf("%w: invalid bundle cursor", ErrInvalid)
	}
	end := min(int64(len(f.bundle.Sources)), after+int64(feedPageSize(limit)))
	out := archive.SourcePage{AfterID: end, Done: end == int64(len(f.bundle.Sources)), IDs: []string{}}
	for _, source := range f.bundle.Sources[after:end] {
		out.IDs = append(out.IDs, source.Record.RequestID)
	}
	return out, nil
}
func (f *BundleFeed) MemoryChanges(ctx context.Context, after int64, limit int) ([]archive.SourceChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, fmt.Errorf("%w: invalid bundle change cursor", ErrInvalid)
	}
	start := sort.Search(len(f.changes), func(i int) bool { return f.changes[i].Sequence > after })
	end := min(len(f.changes), start+feedPageSize(limit))
	return append([]archive.SourceChange{}, f.changes[start:end]...), nil
}

type ScanCursor struct {
	ArchiveID       string `json:"archive_id"`
	Watermark       int64  `json:"watermark"`
	BackfillAfterID int64  `json:"backfill_after_id"`
	BackfillDone    bool   `json:"backfill_done"`
	ChangeAfter     int64  `json:"change_after"`
	Incomplete      bool   `json:"incomplete,omitempty"`
}
type ScanOptions struct {
	Selection Selection
	Cursor    ScanCursor
	PageSize  int
	MaxPages  int
}
type ScanPage struct {
	Inputs   []Input    `json:"inputs"`
	Cursor   ScanCursor `json:"cursor"`
	CaughtUp bool       `json:"caught_up"`
	Complete bool       `json:"complete"`
	Partial  bool       `json:"partial"`
	Issues   []string   `json:"issues"`
	Sources  int        `json:"sources"`
	Deferred int        `json:"deferred"`
}

// Scan returns a proposed cursor, never persists it. The caller must commit all
// returned inputs durably before saving that cursor. The fence bounds events,
// not historical record versions; sources newer than it are explicitly deferred.
func Scan(ctx context.Context, feed storage.MemoryFeed, options ScanOptions) (ScanPage, error) {
	out := ScanPage{Cursor: options.Cursor, Inputs: []Input{}, Issues: []string{}}
	if feed == nil || !validSelection(options.Selection) || options.Cursor.Watermark < 0 || options.Cursor.BackfillAfterID < 0 || options.Cursor.ChangeAfter < 0 || options.Cursor.ChangeAfter > options.Cursor.Watermark {
		return ScanPage{}, fmt.Errorf("%w: invalid scan options", ErrInvalid)
	}
	watermark, err := feed.MemoryWatermark(ctx)
	if err != nil {
		return ScanPage{}, err
	}
	if watermark < out.Cursor.Watermark {
		return ScanPage{}, fmt.Errorf("%w: archive watermark moved backwards", ErrInvalid)
	}
	if identityFeed, ok := feed.(storage.IdentityFeed); ok {
		identity, err := identityFeed.MemoryIdentity(ctx)
		if err != nil {
			return ScanPage{}, err
		}
		if !validArchiveID(identity.ArchiveID) || identity.Watermark < out.Cursor.Watermark || (out.Cursor.ArchiveID != "" && out.Cursor.ArchiveID != identity.ArchiveID) {
			return ScanPage{}, fmt.Errorf("%w: archive identity changed", ErrInvalid)
		}
		out.Cursor.ArchiveID = identity.ArchiveID
	}
	if !validArchiveID(out.Cursor.ArchiveID) {
		return ScanPage{}, fmt.Errorf("%w: an explicit archive identity is required", ErrInvalid)
	}
	if (out.Cursor.Watermark == 0 && out.Cursor.BackfillAfterID == 0 && !out.Cursor.BackfillDone) || (out.Cursor.BackfillDone && out.Cursor.ChangeAfter >= out.Cursor.Watermark) {
		out.Cursor.Watermark = watermark
	}
	pageSize := feedPageSize(options.PageSize)
	maxPages := options.MaxPages
	if maxPages < 1 {
		maxPages = 1
	}
	if maxPages > MaxScanPages {
		return ScanPage{}, fmt.Errorf("%w: scan exceeds page budget", ErrBudget)
	}
	issue := func(value string) {
		out.Partial = true
		for _, existing := range out.Issues {
			if existing == value {
				return
			}
		}
		out.Issues = append(out.Issues, value)
	}
	coverageIssue := func(value string) {
		out.Cursor.Incomplete = true
		issue(value)
	}
	if out.Cursor.Incomplete {
		issue("an earlier scan page had incomplete source coverage; a full rescan is required to re-establish completeness")
	}
	seen := map[string]int64{}
	inputBytes := 0
	read := func(id string, minimumSequence int64) error {
		if sequence, found := seen[id]; found {
			if sequence >= 0 && sequence < minimumSequence {
				return fmt.Errorf("%w: source predates its change event", ErrInvalid)
			}
			return nil
		}
		out.Sources++
		if out.Sources > MaxScanSources {
			return fmt.Errorf("%w: scan exceeds source limit", ErrBudget)
		}
		source, err := feed.MemorySource(ctx, id)
		if errors.Is(err, storage.ErrSourceNotFound) {
			seen[id] = -1
			coverageIssue("a source was deleted before it could be read")
			return nil
		}
		if err != nil {
			return err
		}
		if err := sourceSize(source); err != nil {
			return err
		}
		if source.Record.RequestID != id || !validSnapshot(source) || source.Sequence < minimumSequence {
			return fmt.Errorf("%w: inconsistent source snapshot", ErrInvalid)
		}
		seen[id] = source.Sequence
		if source.Sequence > out.Cursor.Watermark {
			out.Deferred++
			coverageIssue("a source newer than the scan fence was deferred")
			return nil
		}
		inputs, err := InputsFromSnapshot(source, out.Cursor.ArchiveID, options.Selection)
		if err != nil {
			return err
		}
		if len(inputs) == 0 && inSelection(source.Record, options.Selection) && !memory.IsInternalSource(source.Record) && memory.Normalize(source.Record).Partial {
			coverageIssue("retained source coverage is partial")
		}
		for _, input := range inputs {
			raw, err := json.Marshal(input)
			if err != nil {
				return fmt.Errorf("%w: source input cannot be encoded", ErrInvalid)
			}
			inputBytes += len(raw)
			if inputBytes > MaxScanBytes || len(out.Inputs)+len(inputs) > MaxScanInputs {
				return fmt.Errorf("%w: scan exceeds input limit", ErrBudget)
			}
			for _, excerpt := range input.Excerpts {
				if excerpt.Partial {
					coverageIssue("retained source coverage is partial")
				}
			}
		}
		out.Inputs = append(out.Inputs, inputs...)
		return nil
	}
	for page := 0; page < maxPages; page++ {
		if !out.Cursor.BackfillDone {
			batch, err := feed.MemoryBackfill(ctx, out.Cursor.BackfillAfterID, pageSize)
			if err != nil {
				return ScanPage{}, err
			}
			if len(batch.IDs) > pageSize || batch.AfterID < out.Cursor.BackfillAfterID || (len(batch.IDs) > 0 && batch.AfterID <= out.Cursor.BackfillAfterID) || (len(batch.IDs) == 0 && (!batch.Done || batch.AfterID != out.Cursor.BackfillAfterID)) {
				return ScanPage{}, fmt.Errorf("%w: invalid source page", ErrInvalid)
			}
			pageIDs := map[string]bool{}
			for _, id := range batch.IDs {
				if id == "" || pageIDs[id] {
					return ScanPage{}, fmt.Errorf("%w: empty or duplicate source id", ErrInvalid)
				}
				pageIDs[id] = true
				if err := read(id, 0); err != nil {
					return ScanPage{}, err
				}
			}
			out.Cursor.BackfillAfterID, out.Cursor.BackfillDone = batch.AfterID, batch.Done
			continue
		}
		if out.Cursor.ChangeAfter >= out.Cursor.Watermark {
			break
		}
		changes, err := feed.MemoryChanges(ctx, out.Cursor.ChangeAfter, pageSize)
		if err != nil {
			return ScanPage{}, err
		}
		if len(changes) > pageSize {
			return ScanPage{}, fmt.Errorf("%w: invalid change page", ErrInvalid)
		}
		previous := out.Cursor.ChangeAfter
		for _, change := range changes {
			if change.Sequence <= previous || change.RequestID == "" {
				return ScanPage{}, fmt.Errorf("%w: unordered change page", ErrInvalid)
			}
			previous = change.Sequence
		}
		for _, change := range changes {
			if change.Sequence > out.Cursor.Watermark {
				break
			}
			if err := read(change.RequestID, change.Sequence); err != nil {
				return ScanPage{}, err
			}
			out.Cursor.ChangeAfter = change.Sequence
		}
		if len(changes) < pageSize || (len(changes) > 0 && changes[len(changes)-1].Sequence > out.Cursor.Watermark) {
			out.Cursor.ChangeAfter = out.Cursor.Watermark
		}
	}
	finalWatermark, err := feed.MemoryWatermark(ctx)
	if err != nil {
		return ScanPage{}, err
	}
	if finalWatermark < out.Cursor.Watermark {
		return ScanPage{}, fmt.Errorf("%w: archive watermark moved backwards", ErrInvalid)
	}
	if identityFeed, ok := feed.(storage.IdentityFeed); ok {
		identity, err := identityFeed.MemoryIdentity(ctx)
		if err != nil {
			return ScanPage{}, err
		}
		if identity.ArchiveID != out.Cursor.ArchiveID || identity.Watermark < out.Cursor.Watermark {
			return ScanPage{}, fmt.Errorf("%w: archive identity changed during scan", ErrInvalid)
		}
	}
	out.CaughtUp = out.Cursor.BackfillDone && out.Cursor.ChangeAfter >= out.Cursor.Watermark
	if !out.CaughtUp {
		issue("scan page budget reached before the fence")
	}
	out.Complete = out.CaughtUp && !out.Partial
	return out, nil
}
