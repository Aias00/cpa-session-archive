package memoryextract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/memory"
	"cpa-session-archive/internal/nativehistory"
	"cpa-session-archive/internal/storage"
)

func sourceTestSnapshot(t *testing.T, id string, sequence int64) archive.SourceSnapshot {
	t.Helper()
	record := archive.Record{RequestID: id, SessionID: "s", StartedAt: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Facets: map[string][]string{"project.id": {"p"}, "client": {"codex"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"Use the project cache."}]}`), Response: []byte(`{"choices":[{"message":{"role":"assistant","content":"I propose a bounded cache."}}]}`)}
	return sourceTestReseal(t, archive.SourceSnapshot{Record: record, Sequence: sequence})
}

func sourceTestReseal(t *testing.T, source archive.SourceSnapshot) archive.SourceSnapshot {
	t.Helper()
	var err error
	source.Revision, err = sourceRevision(source.Record)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func sourceTestBundle(t *testing.T, sources ...archive.SourceSnapshot) *BundleFeed {
	t.Helper()
	watermark := int64(0)
	for _, source := range sources {
		watermark = max(watermark, source.Sequence)
	}
	feed, err := NewBundleFeed(Bundle{ArchiveID: "archive-test", Watermark: watermark, Sources: sources})
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

func TestInputsFromSnapshotPreservesUnicodeCitationsAndRoles(t *testing.T) {
	source := sourceTestSnapshot(t, "unicode", 7)
	text := strings.Repeat("缓存🙂<>&\n", 700)
	request := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "Never extract this context."},
		map[string]any{"role": "developer", "content": "Do not extract this either."},
		map[string]any{"role": "user", "content": text},
		map[string]any{"role": "assistant", "content": "I ran a test (an assistant report).", "tool_calls": []any{map[string]any{"id": "call", "type": "function", "function": map[string]any{"name": "exec", "arguments": "test"}}}},
		map[string]any{"role": "tool", "tool_call_id": "call", "content": "The retained tool result."},
	}}
	var err error
	source.Record.OriginalRequest, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	source = sourceTestReseal(t, source)
	feed := sourceTestBundle(t, source)
	reader, err := NewSourceReader(feed, "archive-test", Selection{ProjectID: "p", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p", SessionID: "s"})
	if err != nil || len(inputs) < 3 {
		t.Fatalf("bounded windows: %d %v", len(inputs), err)
	}
	var reconstructed strings.Builder
	assistant, tool := false, false
	for index, input := range inputs {
		raw, err := json.Marshal(input)
		if err != nil || len(raw) > MaxWindowBytes || input.Window != index || input.Sequence != 7 {
			t.Fatalf("window exceeds its bound or loses identity: %d %v", len(raw), err)
		}
		if err := reader.VerifyInput(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		for _, excerpt := range input.Excerpts {
			citation := excerpt.Citation
			if citation.NormalizerVersion != 2 || citation.End-citation.Start > 1600 {
				t.Fatal("invalid normalizer span")
			}
			evidence, err := reader.ReadSource(context.Background(), citation)
			if err != nil || evidence.Text != excerpt.Text || evidence.ProjectID != "p" || evidence.SessionID != "s" {
				t.Fatalf("citation failed exact readback: %v", err)
			}
			switch excerpt.Role {
			case "user":
				reconstructed.WriteString(excerpt.Text)
			case "assistant":
				assistant = true
				if excerpt.Kind != "text" {
					t.Fatal("tool declaration became evidence")
				}
			case "tool":
				tool = true
				if excerpt.Kind != "tool_result" || excerpt.ToolCallID != "call" {
					t.Fatal("tool attribution was lost")
				}
			default:
				t.Fatal("context speaker was extracted")
			}
		}
	}
	if reconstructed.String() != text || !assistant || !tool {
		t.Fatal("windowing omitted text or changed speaker attribution")
	}
}

func TestInputsFromSnapshotNativeSpansAndSummaryExclusion(t *testing.T) {
	source := sourceTestSnapshot(t, "native", 3)
	envelope := nativehistory.Envelope{Version: 1, Client: "codex", Lines: []nativehistory.Line{
		{Raw: `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Use native evidence."}]}}`},
		{Raw: `{"type":"response_item","payload":{"type":"function_call","name":"exec","call_id":"n1","arguments":"{}"}}`},
		{Raw: `{"type":"response_item","payload":{"type":"function_call_output","call_id":"n1","output":"native observed result"}}`},
		{Raw: `{"type":"compacted","payload":{"message":"A synthetic compaction summary."}}`},
	}}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	source.Record.SourceFormat, source.Record.OriginalRequest = nativehistory.Format, raw
	source = sourceTestReseal(t, source)
	inputs, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, input := range inputs {
		for _, excerpt := range input.Excerpts {
			count++
			if !strings.HasPrefix(excerpt.Citation.Pointer, "/lines/") || excerpt.Citation.Side != "original_request" || excerpt.Kind == "summary" || excerpt.Kind == "tool_use" {
				t.Fatal("native provenance was replaced or an excluded kind was extracted")
			}
		}
	}
	if count != 2 {
		t.Fatalf("want user and native tool result only, got %d", count)
	}
}

func TestInputsFromSnapshotRequiresExactProjectSessionAndExplicitTags(t *testing.T) {
	source := sourceTestSnapshot(t, "scope", 1)
	for _, selection := range []Selection{{ProjectID: "other"}, {ProjectID: "p", SessionID: "other"}} {
		inputs, err := InputsFromSnapshot(source, "archive-test", selection)
		if err != nil || len(inputs) != 0 {
			t.Fatal("selection escaped its exact scope")
		}
	}
	source.Record.Facets = nil
	source.Record.ProjectName, source.Record.ProjectPath = "p", "/tmp/p"
	source = sourceTestReseal(t, source)
	inputs, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p"})
	if err != nil || len(inputs) != 0 {
		t.Fatal("folder name was treated as project authority")
	}
	source.Record.Facets = map[string][]string{"git.remote": {"remote"}}
	source = sourceTestReseal(t, source)
	inputs, err = InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "git:remote"})
	if err != nil || len(inputs) == 0 {
		t.Fatal("explicit remote scope was not recognized")
	}
	for _, mutate := range []func(*archive.Record){
		func(r *archive.Record) { r.RequestKind = "memory-extraction" },
		func(r *archive.Record) { r.Facets["request.kind"] = []string{"memory"} },
		func(r *archive.Record) { r.Metadata = map[string]any{"request_kind": "memory-extraction"} },
		func(r *archive.Record) {
			r.OriginalRequest = []byte(`{"metadata":{"request_kind":"memory-extraction"},"input":"Must be excluded"}`)
		},
	} {
		internal := sourceTestSnapshot(t, "internal", 1)
		mutate(&internal.Record)
		internal = sourceTestReseal(t, internal)
		inputs, err := InputsFromSnapshot(internal, "archive-test", Selection{ProjectID: "p"})
		if err != nil || len(inputs) != 0 {
			t.Fatal("explicit internal request tag was ignored")
		}
	}
}

func TestSourceReaderRejectsForgedInputAndStaleOrWrongScopeEvidence(t *testing.T) {
	ctx := context.Background()
	source := sourceTestSnapshot(t, "verify", 1)
	feed := sourceTestBundle(t, source)
	reader, err := NewSourceReader(feed, "archive-test", Selection{ProjectID: "p", SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p", SessionID: "s"})
	if err != nil || len(inputs) != 1 {
		t.Fatalf("input: %v", err)
	}
	for name, mutate := range map[string]func(*Input){
		"role":     func(in *Input) { in.Excerpts[0].Role = "tool" },
		"kind":     func(in *Input) { in.Excerpts[0].Kind = "tool_result" },
		"text":     func(in *Input) { in.Excerpts[0].Text = "Invented evidence" },
		"citation": func(in *Input) { in.Excerpts[0].Citation.Start++ },
		"tool":     func(in *Input) { in.Excerpts[0].ToolName = "exec" },
		"partial":  func(in *Input) { in.Excerpts[0].Partial = true },
		"id":       func(in *Input) { in.Excerpts[0].ID = "fake" },
		"scope":    func(in *Input) { in.ProjectID = "other" },
		"session":  func(in *Input) { in.SessionID = "other" },
		"archive":  func(in *Input) { in.ArchiveID = "other" },
		"version":  func(in *Input) { in.Version = "other" },
		"revision": func(in *Input) { in.Revision = strings.Repeat("0", 64) },
		"sequence": func(in *Input) { in.Sequence++ },
		"window":   func(in *Input) { in.Window++ },
	} {
		t.Run(name, func(t *testing.T) {
			in := inputs[0]
			in.Excerpts = append([]Excerpt(nil), in.Excerpts...)
			mutate(&in)
			if err := reader.VerifyInput(ctx, in); !errors.Is(err, memory.ErrSourceUnavailable) {
				t.Fatalf("forged source input did not report authoritative unavailability: %v", err)
			}
		})
	}
	enriched := inputs[0]
	enriched.KnowledgeComplete = true
	enriched.Existing = []memory.MemoryEntry{{}}
	if err := reader.VerifyInput(ctx, enriched); err != nil {
		t.Fatalf("separate knowledge enrichment changed source verification: %v", err)
	}
	citation := inputs[0].Excerpts[0].Citation
	for name, mutate := range map[string]func(*memory.Citation){
		"old normalizer": func(c *memory.Citation) { c.NormalizerVersion = 1 },
		"old revision":   func(c *memory.Citation) { c.Revision = "old" },
		"wrong pointer":  func(c *memory.Citation) { c.Pointer = "/absent" },
		"negative range": func(c *memory.Citation) { c.Start = -1 },
		"large range":    func(c *memory.Citation) { c.End = c.Start + 1601 },
	} {
		t.Run(name, func(t *testing.T) {
			c := citation
			mutate(&c)
			if _, err := reader.ReadSource(ctx, c); !errors.Is(err, memory.ErrSourceUnavailable) {
				t.Fatalf("invalid citation was accepted: %v", err)
			}
		})
	}
	wrong, _ := NewSourceReader(feed, "other-archive", Selection{ProjectID: "p"})
	if _, err := wrong.ReadSource(ctx, citation); !errors.Is(err, memory.ErrSourceUnavailable) {
		t.Fatal("archive identity changed without rejection")
	}
	wrong, _ = NewSourceReader(feed, "archive-test", Selection{ProjectID: "other"})
	if _, err := wrong.ReadSource(ctx, citation); !errors.Is(err, memory.ErrSourceUnavailable) {
		t.Fatal("cross-project source was disclosed")
	}
	wrong, _ = NewSourceReader(feed, "archive-test", Selection{ProjectID: "p", SessionID: "other"})
	if _, err := wrong.ReadSource(ctx, citation); !errors.Is(err, memory.ErrSourceUnavailable) {
		t.Fatal("cross-session source was disclosed")
	}
}

func TestBundleFeedUsesArchiveRevisionAndOwnsCopies(t *testing.T) {
	ctx := context.Background()
	store, err := archive.OpenStore(filepath.Join(t.TempDir(), "archive.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.DB.Close()
	record := sourceTestSnapshot(t, "stored", 1).Record
	record.Metadata = map[string]any{"large_integer": json.Number("9007199254740993")}
	if err := store.PutBatch([]archive.Record{record}); err != nil {
		t.Fatal(err)
	}
	source, err := store.MemorySource(ctx, "stored")
	if err != nil {
		t.Fatal(err)
	}
	feed := sourceTestBundle(t, source)
	identity, err := feed.MemoryIdentity(ctx)
	if err != nil || identity.ArchiveID != "archive-test" || identity.HistoryComplete {
		t.Fatal("offline snapshot claimed a complete historical mutation log")
	}
	source.Record.OriginalRequest[0] = '!'
	read, err := feed.MemorySource(ctx, "stored")
	if err != nil || !validSnapshot(read) {
		t.Fatal("caller mutation changed frozen source revision")
	}
	read.Record.Facets["project.id"][0] = "changed"
	again, err := feed.MemorySource(ctx, "stored")
	if err != nil || !validSnapshot(again) || again.Record.Facets["project.id"][0] != "p" {
		t.Fatal("returned maps changed the immutable feed")
	}
	if _, err := feed.MemorySource(ctx, "absent"); !errors.Is(err, storage.ErrSourceNotFound) {
		t.Fatal("absence did not use authoritative sentinel")
	}
	if _, err := InputsFromSnapshot(again, "archive-test", Selection{ProjectID: "p"}); err != nil {
		t.Fatalf("archive's exact revision algorithm was not reproduced: %v", err)
	}
}

func TestBundleFeedRejectsCorruptionAndOversizeInput(t *testing.T) {
	source := sourceTestSnapshot(t, "a", 2)
	base := Bundle{ArchiveID: "archive-test", Watermark: 2, Sources: []archive.SourceSnapshot{source}}
	for name, mutate := range map[string]func(*Bundle){
		"empty identity":     func(b *Bundle) { b.ArchiveID = "" },
		"negative fence":     func(b *Bundle) { b.Watermark = -1 },
		"future sequence":    func(b *Bundle) { b.Watermark = 1 },
		"negative sequence":  func(b *Bundle) { b.Sources[0].Sequence = -1 },
		"bad revision":       func(b *Bundle) { b.Sources[0].Revision = "invented" },
		"duplicate ID":       func(b *Bundle) { b.Sources = append(b.Sources, b.Sources[0]) },
		"duplicate sequence": func(b *Bundle) { other := sourceTestSnapshot(t, "b", 2); b.Sources = append(b.Sources, other) },
	} {
		t.Run(name, func(t *testing.T) {
			bundle := base
			bundle.Sources = append([]archive.SourceSnapshot(nil), bundle.Sources...)
			mutate(&bundle)
			if _, err := NewBundleFeed(bundle); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid bundle accepted: %v", err)
			}
		})
	}
	raw, _ := json.Marshal(base)
	if _, err := DecodeBundle(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeBundle(strings.NewReader(string(raw) + "{}")); !errors.Is(err, ErrInvalid) {
		t.Fatal("trailing data accepted")
	}
	if _, err := DecodeBundle(strings.NewReader(strings.Repeat(" ", MaxBundleBytes+1))); !errors.Is(err, ErrInvalid) {
		t.Fatal("oversize bundle accepted")
	}
}

// scanTestFeed can change the live source independently of its event fence.
type scanTestFeed struct {
	storage.MemoryFeed
	source    func(context.Context, string) (archive.SourceSnapshot, error)
	watermark int64
	identity  string
	changes   []archive.SourceChange
	changeErr error
}

func (f *scanTestFeed) MemorySource(ctx context.Context, id string) (archive.SourceSnapshot, error) {
	if f.source != nil {
		return f.source(ctx, id)
	}
	return f.MemoryFeed.MemorySource(ctx, id)
}
func (f *scanTestFeed) MemoryWatermark(ctx context.Context) (int64, error) {
	return f.watermark, ctx.Err()
}
func (f *scanTestFeed) MemoryIdentity(ctx context.Context) (storage.FeedIdentity, error) {
	return storage.FeedIdentity{ArchiveID: f.identity, Watermark: f.watermark}, ctx.Err()
}
func (f *scanTestFeed) MemoryChanges(ctx context.Context, after int64, limit int) ([]archive.SourceChange, error) {
	if f.changeErr != nil {
		return nil, f.changeErr
	}
	if f.changes == nil {
		return f.MemoryFeed.MemoryChanges(ctx, after, limit)
	}
	out := []archive.SourceChange{}
	for _, change := range f.changes {
		if change.Sequence > after {
			out = append(out, change)
		}
	}
	return out[:min(len(out), limit)], nil
}

func TestScanResumesBoundedBackfillThenChangesWithoutPersistingCursor(t *testing.T) {
	feed := sourceTestBundle(t, sourceTestSnapshot(t, "a", 1), sourceTestSnapshot(t, "b", 2), sourceTestSnapshot(t, "c", 3))
	options := ScanOptions{Selection: Selection{ProjectID: "p", SessionID: "s"}, PageSize: 1, MaxPages: 1}
	first, err := Scan(context.Background(), feed, options)
	if err != nil || len(first.Inputs) != 1 || first.Cursor.BackfillAfterID != 1 || first.Complete || first.CaughtUp || !first.Partial {
		t.Fatalf("first bounded page: %+v %v", first, err)
	}
	if options.Cursor != (ScanCursor{}) {
		t.Fatal("Scan mutated caller's persisted cursor")
	}
	options.Cursor = first.Cursor
	options.MaxPages = 8
	next, err := Scan(context.Background(), feed, options)
	if err != nil || !next.Complete || !next.CaughtUp || next.Partial || next.Cursor.ChangeAfter != 3 || !next.Cursor.BackfillDone {
		t.Fatalf("resumed scan: %+v %v", next, err)
	}
	requests := map[string]bool{}
	for _, input := range append(first.Inputs, next.Inputs...) {
		requests[input.RequestID] = true
	}
	if len(requests) != 3 {
		t.Fatal("bounded resume lost a source")
	}
	options.Cursor = next.Cursor
	empty, err := Scan(context.Background(), feed, options)
	if err != nil || len(empty.Inputs) != 0 || !empty.Complete {
		t.Fatalf("caught-up scan replayed sources: %v", err)
	}
}

func TestScanDefersNewerSnapshotAndLaterProcessesItsEvent(t *testing.T) {
	old := sourceTestSnapshot(t, "a", 1)
	newer := sourceTestSnapshot(t, "a", 2)
	feed := &scanTestFeed{MemoryFeed: sourceTestBundle(t, old), watermark: 1, identity: "archive-test", changes: []archive.SourceChange{{Sequence: 1, RequestID: "a"}, {Sequence: 2, RequestID: "a"}}}
	feed.source = func(context.Context, string) (archive.SourceSnapshot, error) { return newer, nil }
	options := ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 8}
	first, err := Scan(context.Background(), feed, options)
	if err != nil || len(first.Inputs) != 0 || first.Deferred != 1 || first.Complete || !first.Partial || !first.CaughtUp || first.Cursor.ChangeAfter != 1 || !first.Cursor.Incomplete {
		t.Fatalf("future snapshot was presented as fence-stable: %+v %v", first, err)
	}
	feed.watermark = 2
	options.Cursor = first.Cursor
	next, err := Scan(context.Background(), feed, options)
	if err != nil || len(next.Inputs) == 0 || next.Inputs[0].Sequence != 2 || next.Cursor.ChangeAfter != 2 || !next.CaughtUp || next.Complete {
		t.Fatalf("deferred source was lost or earlier coverage gap hidden: %+v %v", next, err)
	}
}

func TestScanRetainsPartialCoverageAcrossPagesAndRejectsTransportFailure(t *testing.T) {
	partial := sourceTestSnapshot(t, "a-partial", 1)
	partial.Record.Response = nil
	partial = sourceTestReseal(t, partial)
	complete := sourceTestSnapshot(t, "b-complete", 2)
	feed := sourceTestBundle(t, partial, complete)
	options := ScanOptions{Selection: Selection{ProjectID: "p"}, PageSize: 1, MaxPages: 1}
	first, err := Scan(context.Background(), feed, options)
	if err != nil || !first.Cursor.Incomplete {
		t.Fatalf("partial payload was hidden: %v", err)
	}
	options.Cursor, options.MaxPages = first.Cursor, 8
	last, err := Scan(context.Background(), feed, options)
	if err != nil || last.Complete || !last.Partial || !last.CaughtUp {
		t.Fatalf("later page erased earlier missing coverage: %v", err)
	}
	transport := errors.New("synthetic transport failure")
	broken := &scanTestFeed{MemoryFeed: feed, watermark: 2, identity: "archive-test", source: func(context.Context, string) (archive.SourceSnapshot, error) {
		return archive.SourceSnapshot{}, transport
	}}
	failed, err := Scan(context.Background(), broken, ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 8})
	if !errors.Is(err, transport) || !reflect.DeepEqual(failed, ScanPage{}) {
		t.Fatal("transport failure advanced discovery or became a tombstone")
	}
	broken.source = func(context.Context, string) (archive.SourceSnapshot, error) {
		return archive.SourceSnapshot{}, storage.ErrSourceNotFound
	}
	deleted, err := Scan(context.Background(), broken, ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 8})
	if err != nil || deleted.Complete || !deleted.Partial || !deleted.CaughtUp {
		t.Fatalf("authoritative deletion was not disclosed: %v", err)
	}
}

func TestScanRejectsChangedIdentityRollbackAndUnorderedEvents(t *testing.T) {
	feed := sourceTestBundle(t, sourceTestSnapshot(t, "a", 1))
	options := ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 8, Cursor: ScanCursor{ArchiveID: "old-archive", Watermark: 1}}
	if _, err := Scan(context.Background(), feed, options); !errors.Is(err, ErrInvalid) {
		t.Fatal("changed archive identity accepted")
	}
	options.Cursor = ScanCursor{ArchiveID: "archive-test", Watermark: 2, BackfillDone: true, ChangeAfter: 1}
	if _, err := Scan(context.Background(), feed, options); !errors.Is(err, ErrInvalid) {
		t.Fatal("archive rollback accepted")
	}
	broken := &scanTestFeed{MemoryFeed: feed, watermark: 3, identity: "archive-test", changes: []archive.SourceChange{{Sequence: 2, RequestID: "a"}, {Sequence: 1, RequestID: "a"}}}
	options.Cursor = ScanCursor{ArchiveID: "archive-test", Watermark: 3, BackfillDone: true}
	if _, err := Scan(context.Background(), broken, options); !errors.Is(err, ErrInvalid) {
		t.Fatal("unordered change stream accepted")
	}
}

func TestSourceBudgetsReturnNoAdvanceableCursor(t *testing.T) {
	source := sourceTestSnapshot(t, "large", 1)
	source.Record.OriginalRequest = []byte(`{"input":"` + strings.Repeat("a", MaxSourceBytes) + `"}`)
	source = sourceTestReseal(t, source)
	if _, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("oversize normalizer input was not bounded: %v", err)
	}
	// JSON escaping can require many small windows even below the raw byte cap.
	source.Record.OriginalRequest = []byte(`{"input":"` + strings.Repeat("<", 150000) + `"}`)
	source = sourceTestReseal(t, source)
	if _, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("excess source windows were not bounded: %v", err)
	}
	sources := make([]archive.SourceSnapshot, MaxScanSources+1)
	for index := range sources {
		sources[index] = sourceTestSnapshot(t, fmt.Sprintf("source-%d", index), int64(index+1))
	}
	feed := sourceTestBundle(t, sources...)
	page, err := Scan(context.Background(), feed, ScanOptions{Selection: Selection{ProjectID: "p"}, PageSize: 256, MaxPages: 2})
	if !errors.Is(err, ErrBudget) || !reflect.DeepEqual(page, ScanPage{}) {
		t.Fatalf("source budget produced an advanceable cursor: %v", err)
	}
	page, err = Scan(context.Background(), feed, ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: MaxScanPages + 1})
	if !errors.Is(err, ErrBudget) || !reflect.DeepEqual(page, ScanPage{}) {
		t.Fatalf("page budget produced an advanceable cursor: %v", err)
	}
	// Many individually bounded sources must also obey the combined input cap.
	sources = nil
	for index := 0; index < 20; index++ {
		source := sourceTestSnapshot(t, fmt.Sprintf("input-%d", index), int64(index+1))
		source.Record.OriginalRequest = []byte(`{"input":"` + strings.Repeat("a", 180000) + `"}`)
		sources = append(sources, sourceTestReseal(t, source))
	}
	page, err = Scan(context.Background(), sourceTestBundle(t, sources...), ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 1})
	if !errors.Is(err, ErrBudget) || !reflect.DeepEqual(page, ScanPage{}) {
		t.Fatalf("combined input budget produced an advanceable cursor: %v", err)
	}
}

func TestScanDoesNotCallEmptyPartialPayloadComplete(t *testing.T) {
	source := sourceTestSnapshot(t, "empty-partial", 1)
	source.Record.OriginalRequest, source.Record.Response = nil, nil
	source = sourceTestReseal(t, source)
	page, err := Scan(context.Background(), sourceTestBundle(t, source), ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 8})
	if err != nil || len(page.Inputs) != 0 || !page.Partial || page.Complete || !page.Cursor.Incomplete {
		t.Fatalf("missing payload was presented as a complete empty extraction: %v", err)
	}
}

func TestSourceReaderPreservesTransportErrors(t *testing.T) {
	source := sourceTestSnapshot(t, "a", 1)
	transport := errors.New("synthetic authentication failure")
	feed := &scanTestFeed{MemoryFeed: sourceTestBundle(t, source), watermark: 1, identity: "archive-test", source: func(context.Context, string) (archive.SourceSnapshot, error) {
		return archive.SourceSnapshot{}, transport
	}}
	reader, err := NewSourceReader(feed, "archive-test", Selection{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.VerifyInput(context.Background(), inputs[0]); !errors.Is(err, transport) || errors.Is(err, memory.ErrSourceUnavailable) {
		t.Fatal("transport failure became an authoritative absence")
	}
	if _, err := reader.ReadSource(context.Background(), inputs[0].Excerpts[0].Citation); !errors.Is(err, transport) || errors.Is(err, memory.ErrSourceUnavailable) {
		t.Fatal("read transport failure became an authoritative absence")
	}
}

func TestBundleBackfillCursorSurvivesSourceArrayReordering(t *testing.T) {
	a, b := sourceTestSnapshot(t, "a", 1), sourceTestSnapshot(t, "b", 2)
	first := sourceTestBundle(t, b, a)
	second := sourceTestBundle(t, a, b)
	page, err := first.MemoryBackfill(context.Background(), 0, 1)
	if err != nil || len(page.IDs) != 1 || page.IDs[0] != "a" {
		t.Fatal("bundle backfill does not have a deterministic order")
	}
	next, err := second.MemoryBackfill(context.Background(), page.AfterID, 1)
	if err != nil || len(next.IDs) != 1 || next.IDs[0] != "b" {
		t.Fatal("reordering the source array changed a persisted cursor")
	}
}

func TestScanRejectsMalformedEventsPastFenceAndOlderSnapshots(t *testing.T) {
	source := sourceTestSnapshot(t, "a", 1)
	feed := &scanTestFeed{MemoryFeed: sourceTestBundle(t, source), watermark: 3, identity: "archive-test"}
	options := ScanOptions{Selection: Selection{ProjectID: "p"}, MaxPages: 1, Cursor: ScanCursor{ArchiveID: "archive-test", Watermark: 3, BackfillDone: true}}
	feed.changes = []archive.SourceChange{{Sequence: 4, RequestID: "a"}, {Sequence: 2, RequestID: "a"}}
	if _, err := Scan(context.Background(), feed, options); !errors.Is(err, ErrInvalid) {
		t.Fatal("an out-of-order event after a future event was ignored")
	}
	feed.changes = []archive.SourceChange{{Sequence: 2, RequestID: "a"}}
	if _, err := Scan(context.Background(), feed, options); !errors.Is(err, ErrInvalid) {
		t.Fatal("a source older than its change event was used as evidence")
	}
}

func TestInputsFromSnapshotSkipsBlankOnlyWindowsWithoutChangingOffsets(t *testing.T) {
	source := sourceTestSnapshot(t, "blank-runs", 1)
	text := strings.Repeat(" ", 5000) + "First retained user evidence." + strings.Repeat("\t\n", 3000) + "Second retained user evidence." + strings.Repeat(" ", 5000)
	raw, err := json.Marshal(map[string]string{"input": text})
	if err != nil {
		t.Fatal(err)
	}
	source.Record.OriginalRequest = raw
	source = sourceTestReseal(t, source)
	inputs, err := InputsFromSnapshot(source, "archive-test", Selection{ProjectID: "p"})
	if err != nil || len(inputs) == 0 {
		t.Fatalf("blank runs prevented extraction: %v", err)
	}
	reader, err := NewSourceReader(sourceTestBundle(t, source), "archive-test", Selection{ProjectID: "p"})
	if err != nil {
		t.Fatal(err)
	}
	first, second := false, false
	for index, input := range inputs {
		if input.Window != index || validateShape(input) != nil {
			t.Fatal("source adapter emitted a window the ledger cannot enqueue")
		}
		if err := reader.VerifyInput(context.Background(), input); err != nil {
			t.Fatal(err)
		}
		for _, excerpt := range input.Excerpts {
			evidence, err := reader.ReadSource(context.Background(), excerpt.Citation)
			if err != nil || evidence.Text != excerpt.Text || strings.TrimSpace(excerpt.Text) == "" {
				t.Fatalf("skipping blank spans changed exact source offsets: %v", err)
			}
			first = first || strings.Contains(excerpt.Text, "First retained user evidence.")
			second = second || strings.Contains(excerpt.Text, "Second retained user evidence.")
		}
	}
	if !first || !second {
		t.Fatal("blank-span removal lost nonblank evidence")
	}
}
