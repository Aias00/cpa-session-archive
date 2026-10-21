export type Locale = "zh" | "en";
export type ApiClient = <T>(path: string, options?: RequestInit) => Promise<T>;
export type ProjectFilter = { name: string; value: string };
export type AutomaticView = "all" | "recent" | "inactive" | "main" | "subagent" | "system" | "prewarm" | "unassigned";
export type LibraryView = AutomaticView | "starred" | "archived";
export interface BulkSelection {
  q?: string;
  tag?: string;
  archived: "exclude" | "only" | "all";
  starred?: boolean;
  filters: Record<string, string>;
  view: AutomaticView;
  inactive_days: number;
}
export interface BulkAction {
  archived?: boolean;
  starred?: boolean;
  add_tags?: string[];
  remove_tags?: string[];
  include_starred?: boolean;
  include_edited?: boolean;
}
export interface BulkJob {
  id: string;
  status: "preview" | "running" | "complete" | "undoing" | "undone";
  selection: BulkSelection;
  action: BulkAction;
  total: number;
  pending: number;
  applied: number;
  conflicts: number;
  protected: number;
  unchanged: number;
  undone: number;
  undo_conflicts: number;
  cancelled: number;
  created_at: string;
  updated_at: string;
  items?: { session_id: string; status: string; reason?: string }[];
}

export interface FacetCount { name: string; value: string; sessions: number }
export interface SessionSummary {
  session_id: string;
  requests: number;
  first_at?: string;
  last_at: string;
  key_id?: string;
  principal_id?: string;
  principal_alias?: string;
  credential_hashes?: string[];
  model?: string;
  project?: string;
  summary?: string;
  kinds?: string[];
  thread_sources?: string[];
}
export interface SessionManagement {
  session_id: string;
  title: string;
  note: string;
  tags: string[];
  starred: boolean;
  archived: boolean;
  revision: number;
  updated_at: string;
}
export interface ManagedSession extends SessionSummary { management: SessionManagement }
export interface ManagedSessionPage { sessions: ManagedSession[]; total: number; limit: number; offset: number }
export interface ArchiveStats { records: number; sessions: number; blobs: number; logical_bytes: number; compressed_bytes: number; saved_bytes: number }

export function emptyManagement(sessionID: string): SessionManagement {
  return { session_id: sessionID, title: "", note: "", tags: [], starred: false, archived: false, revision: 0, updated_at: "" };
}
