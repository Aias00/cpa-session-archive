import type { ApiClient } from "./types";

export interface Connection { base: string; key: string }
export class ApiError extends Error {
  constructor(public status: number) { super(`HTTP ${status}`); }
}

// Support the host panel's existing storage envelope without persisting a new key.
export function hostConnection(): Connection | null {
  try {
    let raw = localStorage.getItem("cli-proxy-auth");
    if (!raw) return null;
    if (raw.startsWith("enc::v1::")) {
      const bytes = Uint8Array.from(atob(raw.slice(9)), (value) => value.charCodeAt(0));
      const mask = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${location.host}|${navigator.userAgent}`);
      raw = new TextDecoder().decode(bytes.map((value, index) => value ^ mask[index % mask.length]));
    }
    const envelope = JSON.parse(raw) as { state?: Record<string, unknown>; apiBase?: string; managementKey?: string };
    const value = envelope.state || envelope;
    if (typeof value.apiBase === "string" && typeof value.managementKey === "string")
      return { base: value.apiBase.replace(/\/$/, ""), key: value.managementKey };
  } catch { /* The connect form handles absent or unreadable host state. */ }
  return null;
}

export function validateConnection(connection: Connection): Connection {
  const url = new URL(connection.base);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password || url.search || url.hash || (url.pathname !== "/" && url.pathname !== ""))
    throw new Error("Use a CPA management origin without a path or embedded credential.");
  if (url.protocol === "http:" && !["127.0.0.1", "[::1]", "localhost"].includes(url.hostname))
    throw new Error("Use HTTPS or a loopback HTTP endpoint.");
  if (!connection.key.trim()) throw new Error("A management credential is required.");
  return { base: url.origin, key: connection.key.trim() };
}

export function createApi(connection: Connection): ApiClient {
  return async <T>(path: string, options: RequestInit = {}): Promise<T> => {
    const target = path.startsWith("/v0/") ? path : `/v0/management/plugins/cpa-session-archive${path}`;
    const headers = new Headers(options.headers);
    headers.set("Authorization", `Bearer ${connection.key}`);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(connection.base + target, { ...options, headers, redirect: "error", cache: "no-store" });
    if (!response.ok) throw new ApiError(response.status);
    return await response.json() as T;
  };
}
