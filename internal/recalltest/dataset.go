// Package recalltest evaluates frozen, owner-controlled retrieval evidence labels.
// It operates only on isolated fixture archives; it never opens a live archive.
package recalltest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"cpa-session-archive/internal/memory"
)

const MaxDatasetBytes = 64 << 20

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Record struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	SessionID string    `json:"session_id"`
	Client    string    `json:"client"`
	EventAt   string    `json:"event_at"`
	Messages  []Message `json:"messages"`
	Note      string    `json:"note,omitempty"`
}
type Evidence struct {
	RecordID string   `json:"record_id"`
	Contains []string `json:"contains"`
	Side     string   `json:"side,omitempty"`
	Pointer  string   `json:"pointer,omitempty"`
}
type Case struct {
	ID           string       `json:"id"`
	Category     string       `json:"category"`
	Query        memory.Query `json:"query"`
	RelevantIDs  []string     `json:"relevant_ids,omitempty"`
	Evidence     []Evidence   `json:"evidence,omitempty"`
	ForbiddenIDs []string     `json:"forbidden_ids,omitempty"`
	ExpectEmpty  bool         `json:"expect_empty,omitempty"`
	Note         string       `json:"note,omitempty"`
}
type Change struct {
	Kind           string  `json:"kind"`
	ID             string  `json:"id"`
	Record         *Record `json:"record,omitempty"`
	SyncProjection bool    `json:"sync_projection"`
}
type Dataset struct {
	Version       int      `json:"version"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Records       []Record `json:"records"`
	Cases         []Case   `json:"cases"`
	Changes       []Change `json:"changes,omitempty"`
	fileDigest    string
	contentDigest string
}

// Load bounds input and rejects ambiguous/trailing values. Errors refer to
// numeric positions, never private queries, evidence, labels or dataset names.
func Load(reader io.Reader) (Dataset, error) {
	var dataset Dataset
	raw, err := io.ReadAll(io.LimitReader(reader, MaxDatasetBytes+1))
	if err != nil || len(raw) > MaxDatasetBytes {
		return dataset, errors.New("evaluation dataset unreadable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&dataset) != nil {
		return Dataset{}, errors.New("invalid evaluation dataset JSON")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return Dataset{}, errors.New("evaluation dataset must contain one JSON value")
	}
	if err = dataset.Validate(); err != nil {
		return Dataset{}, err
	}
	digest := sha256.Sum256(raw)
	dataset.fileDigest = hex.EncodeToString(digest[:])
	dataset.contentDigest = datasetDigest(dataset)
	return dataset, nil
}
func datasetDigest(dataset Dataset) string {
	raw, _ := json.Marshal(dataset)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
func fixtureClient(record Record) string {
	if record.Client == "" {
		return "codex"
	}
	return record.Client
}
func validRecord(record Record) bool {
	if record.ID == "" || record.ProjectID == "" || record.SessionID == "" || len(record.Messages) == 0 {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, record.EventAt); err != nil {
		return false
	}
	for _, message := range record.Messages {
		switch message.Role {
		case "user", "assistant", "tool", "system", "developer":
		default:
			return false
		}
		if message.Content == "" {
			return false
		}
	}
	return true
}
func (dataset Dataset) Validate() error {
	if dataset.Version != 1 || len(dataset.Records) == 0 || len(dataset.Records) > 10000 || len(dataset.Cases) == 0 || len(dataset.Cases) > 10000 {
		return errors.New("unsupported evaluation dataset version or size")
	}
	known := map[string]Record{}
	active := map[string]Record{}
	for index, record := range dataset.Records {
		if !validRecord(record) {
			return fmt.Errorf("invalid fixture record at position %d", index)
		}
		if _, exists := known[record.ID]; exists {
			return fmt.Errorf("duplicate fixture record at position %d", index)
		}
		known[record.ID] = record
		active[record.ID] = record
	}
	for index, change := range dataset.Changes {
		if _, exists := active[change.ID]; !exists {
			return fmt.Errorf("change references unavailable record at position %d", index)
		}
		switch change.Kind {
		case "delete":
			if change.Record != nil {
				return fmt.Errorf("delete has replacement at position %d", index)
			}
			delete(active, change.ID)
		case "replace":
			if change.Record == nil || !validRecord(*change.Record) || change.Record.ID != change.ID {
				return fmt.Errorf("invalid replacement at position %d", index)
			}
			active[change.ID] = *change.Record
		default:
			return fmt.Errorf("unknown change at position %d", index)
		}
	}
	categories := map[string]bool{"exact_error": true, "path": true, "chinese": true, "paraphrase": true, "old_decision": true, "correction": true, "branch": true, "missing": true, "repetition": true}
	seen := map[string]bool{}
	for index, item := range dataset.Cases {
		fail := func(reason string) error { return fmt.Errorf("case at position %d: %s", index, reason) }
		if item.ID == "" || seen[item.ID] || !categories[item.Category] {
			return fail("invalid identity or category")
		}
		seen[item.ID] = true
		if strings.TrimSpace(item.Query.Text) == "" || (item.Query.Limit != 0 && item.Query.Limit != 5) {
			return fail("requires nonempty query and top-five limit")
		}
		if item.ExpectEmpty {
			if len(item.RelevantIDs) != 0 || len(item.Evidence) != 0 {
				return fail("empty expectation has positive labels")
			}
		} else if len(item.RelevantIDs) == 0 {
			return fail("missing positive labels")
		}
		relevant := map[string]bool{}
		for _, id := range item.RelevantIDs {
			record, exists := active[id]
			if !exists || relevant[id] {
				return fail("positive label unavailable or duplicated")
			}
			if (item.Query.ProjectID != "" && item.Query.ProjectID != record.ProjectID) || (item.Query.SessionID != "" && item.Query.SessionID != record.SessionID) || (item.Query.Client != "" && item.Query.Client != fixtureClient(record)) {
				return fail("positive label outside query scope")
			}
			relevant[id] = true
		}
		anchored := map[string]bool{}
		for _, label := range item.Evidence {
			if !relevant[label.RecordID] || len(label.Contains) == 0 {
				return fail("evidence not bound to positive label")
			}
			supported := false
			for messageIndex, message := range active[label.RecordID].Messages {
				if message.Role == "system" || message.Role == "developer" || (label.Side != "" && label.Side != "original_request") || (label.Pointer != "" && label.Pointer != fmt.Sprintf("/messages/%d/content", messageIndex)) {
					continue
				}
				all := true
				for _, anchor := range label.Contains {
					if anchor == "" || !strings.Contains(message.Content, anchor) {
						all = false
						break
					}
				}
				if all {
					supported = true
					break
				}
			}
			if !supported {
				return fail("evidence anchors lack a supporting message")
			}
			anchored[label.RecordID] = true
		}
		for id := range relevant {
			if !anchored[id] {
				return fail("positive source lacks evidence anchors")
			}
		}
		for _, id := range item.ForbiddenIDs {
			if _, exists := known[id]; !exists || relevant[id] {
				return fail("invalid forbidden source label")
			}
		}
	}
	return nil
}
