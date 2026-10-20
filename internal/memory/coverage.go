package memory

import (
	"context"
	"database/sql"
	"sort"
	"strings"
)

// Coverage classification does not change normalized text or citation versions.
// Ambiguous legacy reasons must not be relabeled as intentional exclusions.
type CoverageIssue struct {
	Code     string `json:"code"`
	Category string `json:"category"`
}

type CoverageCount struct {
	CoverageIssue
	Sources int64 `json:"sources"`
}

type CoverageSummary struct {
	Version                      int             `json:"version"`
	SourcesWithIssues            int64           `json:"sources_with_issues"`
	ExpectedOmissionsOnlySources int64           `json:"expected_omissions_only_sources"`
	NeedsInvestigationSources    int64           `json:"needs_investigation_sources"`
	UnexplainedPartialSources    int64           `json:"unexplained_partial_sources"`
	Reasons                      []CoverageCount `json:"reasons"`
}

func classifyCoverageIssue(issue string) CoverageIssue {
	code, category := "unclassified", "unknown"
	switch issue {
	case "tool declarations omitted":
		code, category = "tool_declarations_excluded", "intentional"
	case "media content omitted":
		code, category = "media_excluded", "intentional"
	case "opaque reasoning omitted", "opaque Buddy reasoning retained without indexing":
		code, category = "reasoning_excluded", "intentional"
	case "internal memory request excluded":
		code, category = "internal_request_excluded", "intentional"
	case "auxiliary native source retained without indexing":
		code, category = "auxiliary_source_unclassified", "ambiguous"
	case "compacted context: replacement snapshot not replayed", "compacted Claude context retained", "Claude compaction summary retained without snapshot replay", "compacted Kiro context: snapshot not replayed", "database compaction retained without snapshot replay":
		code, category = "compacted_context", "compacted"
	case "opaque or unsupported Codex response item omitted":
		code, category = "codex_item_unclassified", "ambiguous"
	case "opaque or unsupported native content omitted", "opaque or unsupported content block omitted", "opaque or unsupported database part retained", "opaque SSE content omitted", "opaque SSE delta omitted":
		code, category = "content_unclassified", "ambiguous"
	case "malformed or auxiliary native record retained":
		code, category = "native_record_unclassified", "ambiguous"
	case "Kiro metadata or unsupported event retained without indexing":
		code, category = "kiro_event_unclassified", "ambiguous"
	case "missing original_request", "missing response", "unavailable completion message":
		code, category = "missing_payload", "missing"
	case "retained payload was truncated", "retained native history was truncated":
		code, category = "retained_payload_truncated", "truncated"
	case "upstream request did not complete successfully", "upstream SSE failure", "native client recorded an API error":
		code, category = "upstream_failure", "upstream_failure"
	case "incomplete retained stream":
		code, category = "incomplete_stream", "incomplete"
	case "invalid JSON in original_request", "invalid JSON in response", "messages is not an array", "malformed SSE event", "invalid native history envelope", "invalid database message retained", "invalid database part retained":
		code, category = "malformed_payload", "invalid"
	case "unsupported request protocol", "unsupported response protocol", "unsupported SSE protocol", "unsupported native envelope version", "unsupported database snapshot version":
		code, category = "unsupported_protocol", "unsupported"
	default:
		if strings.HasPrefix(issue, "unsupported ") {
			code, category = "unsupported_content", "unsupported"
		}
	}
	return CoverageIssue{Code: code, Category: category}
}

func classifyCoverage(issues []string) []CoverageIssue {
	out := []CoverageIssue{}
	seen := map[string]bool{}
	for _, issue := range issues {
		if issue == "" {
			continue
		}
		classified := classifyCoverageIssue(issue)
		if !seen[classified.Code] {
			seen[classified.Code] = true
			out = append(out, classified)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// The small grouped issue table avoids scanning sources on each status read.
// SQL triggers keep it correct even when an older writer opens the projection.
func openCoverageStats(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='coverage_groups'`).Scan(&exists); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS coverage_groups(issues_text TEXT PRIMARY KEY,sources INTEGER NOT NULL)`); err != nil {
		return err
	}
	var triggers int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name='sources' AND name IN ('coverage_sources_insert','coverage_sources_delete','coverage_sources_update')`).Scan(&triggers); err != nil {
		return err
	}
	if exists == 0 || triggers != 3 {
		// Older writers may rebuild sources without knowing this extension.
		// Missing triggers mean existing aggregates can no longer be trusted.
		if _, err := tx.Exec(`DELETE FROM coverage_groups`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO coverage_groups SELECT issues_text,COUNT(*) FROM sources WHERE issues_text<>'' GROUP BY issues_text`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
 CREATE TRIGGER IF NOT EXISTS coverage_sources_insert AFTER INSERT ON sources WHEN NEW.issues_text<>'' BEGIN
 INSERT INTO coverage_groups VALUES(NEW.issues_text,1) ON CONFLICT(issues_text) DO UPDATE SET sources=sources+1; END;
 CREATE TRIGGER IF NOT EXISTS coverage_sources_delete AFTER DELETE ON sources WHEN OLD.issues_text<>'' BEGIN
 UPDATE coverage_groups SET sources=sources-1 WHERE issues_text=OLD.issues_text;
 DELETE FROM coverage_groups WHERE issues_text=OLD.issues_text AND sources=0; END;
 CREATE TRIGGER IF NOT EXISTS coverage_sources_update AFTER UPDATE OF issues_text ON sources WHEN OLD.issues_text<>NEW.issues_text BEGIN
 UPDATE coverage_groups SET sources=sources-1 WHERE issues_text=OLD.issues_text;
 DELETE FROM coverage_groups WHERE issues_text=OLD.issues_text AND sources=0;
 INSERT INTO coverage_groups SELECT NEW.issues_text,1 WHERE NEW.issues_text<>'' ON CONFLICT(issues_text) DO UPDATE SET sources=sources+1; END;`); err != nil {
		return err
	}
	return tx.Commit()
}

func coverageStatus(ctx context.Context, tx *sql.Tx, partialSources int64) (*CoverageSummary, error) {
	rows, err := tx.QueryContext(ctx, `SELECT issues_text,sources FROM coverage_groups`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &CoverageSummary{Version: 1, Reasons: []CoverageCount{}}
	counts := map[string]CoverageCount{}
	for rows.Next() {
		var issues string
		var n int64
		if err := rows.Scan(&issues, &n); err != nil {
			return nil, err
		}
		out.SourcesWithIssues += n
		classified := classifyCoverage(strings.Split(issues, "\n"))
		expectedOnly := len(classified) > 0
		for _, issue := range classified {
			count := counts[issue.Code]
			count.CoverageIssue = issue
			count.Sources += n
			counts[issue.Code] = count
			if issue.Category != "intentional" && issue.Category != "compacted" {
				expectedOnly = false
			}
		}
		if expectedOnly {
			out.ExpectedOmissionsOnlySources += n
		} else {
			out.NeedsInvestigationSources += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if partialSources > out.SourcesWithIssues {
		out.UnexplainedPartialSources = partialSources - out.SourcesWithIssues
		out.NeedsInvestigationSources += out.UnexplainedPartialSources
		count := counts["unclassified"]
		count.CoverageIssue = CoverageIssue{Code: "unclassified", Category: "unknown"}
		count.Sources += out.UnexplainedPartialSources
		counts["unclassified"] = count
	}
	for _, count := range counts {
		out.Reasons = append(out.Reasons, count)
	}
	sort.Slice(out.Reasons, func(i, j int) bool { return out.Reasons[i].Code < out.Reasons[j].Code })
	return out, nil
}
