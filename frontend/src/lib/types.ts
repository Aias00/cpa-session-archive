export type Locale = "zh" | "en";
export type ApiClient = <T>(path: string, options?: RequestInit) => Promise<T>;
export type ProjectFilter = { name: string; value: string };
export type LibraryView = "all" | "starred" | "archived";

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
