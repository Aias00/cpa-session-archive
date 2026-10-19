// Package nativehistory imports retained client records without executing them.
package nativehistory

import "time"

const Format = "native-history/v1"

type Context struct {
	AgentKind        string    `json:"agent_kind,omitempty"`
	ParentAgentID    string    `json:"parent_agent_id,omitempty"`
	AgentRole        string    `json:"agent_role,omitempty"`
	IdentityConflict bool      `json:"identity_conflict,omitempty"`
	Title            string    `json:"title,omitempty"`
	SessionID        string    `json:"session_id"`
	ParentID         string    `json:"parent_id,omitempty"`
	Branch           string    `json:"branch,omitempty"`
	CWD              string    `json:"cwd,omitempty"`
	GitRemote        string    `json:"git_remote,omitempty"`
	Model            string    `json:"model,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	CodexResponses   bool      `json:"codex_responses,omitempty"`
	ScopeAmbiguous   bool      `json:"scope_ambiguous,omitempty"`
}
type Line struct {
	Offset int64  `json:"offset"`
	Raw    string `json:"raw"`
}

// Raw includes the original line ending. Pointers address the decoded raw event
// inside each line, not a fictitious HTTP request.
type Envelope struct {
	Version int     `json:"version"`
	Client  string  `json:"client"`
	Context Context `json:"context"`
	Lines   []Line  `json:"lines"`
}
type Message struct {
	Role, Text, Pointer, NativeID                string
	Kind, ToolName, ToolCallID, Model, MessageAt string
}
type Document struct {
	Messages []Message
	Partial  bool
	Issues   []string
}
