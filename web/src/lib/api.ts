import type { AuditEntry, Container, ContainerAction, Enrollment, Host, Me, MetricPoint } from "./types";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// The CSRF token is bound to the session and returned by /auth/me and login.
let csrfToken = "";

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrfToken) headers["X-CSRF-Token"] = csrfToken;
  const res = await fetch("/api" + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  if (res.status === 204) return undefined as T;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, (data as { error?: string }).error ?? res.statusText);
  return data as T;
}

const withCsrf = (me: Me) => {
  csrfToken = me.csrf_token;
  return me;
};

export const api = {
  setupStatus: () => request<{ needs_setup: boolean }>("GET", "/setup/status"),
  setupBegin: (b: { setup_token: string; username: string; password: string }) =>
    request<{ setup_id: string; totp_secret: string; totp_uri: string; qr_png: string }>("POST", "/setup/begin", b),
  setupComplete: (b: { setup_id: string; code: string }) => request<Me>("POST", "/setup/complete", b).then(withCsrf),
  login: (b: { username: string; password: string; code: string }) =>
    request<Me>("POST", "/auth/login", b).then(withCsrf),
  me: () => request<Me>("GET", "/auth/me").then(withCsrf),
  logout: () => request<void>("POST", "/auth/logout"),

  hosts: () => request<Host[]>("GET", "/hosts"),
  host: (id: string) => request<Host>("GET", `/hosts/${id}`),
  createHost: (b: { name: string; address: string }) => request<{ host: Host } & Enrollment>("POST", "/hosts", b),
  updateHost: (id: string, b: { name: string; address: string }) => request<Host>("PATCH", `/hosts/${id}`, b),
  deleteHost: (id: string) => request<void>("DELETE", `/hosts/${id}`),
  newEnrollment: (id: string) => request<Enrollment>("POST", `/hosts/${id}/enrollment`),
  rotateCert: (id: string) => request<void>("POST", `/hosts/${id}/rotate-certificate`),
  metrics: (id: string, range: string) => request<MetricPoint[]>("GET", `/hosts/${id}/metrics?range=${range}`),
  audit: () => request<AuditEntry[]>("GET", "/audit?limit=300"),

  containers: (hostId: string) => request<Container[]>("GET", `/hosts/${hostId}/containers`),
  inspect: (hostId: string, cid: string) => request<Record<string, unknown>>("GET", `/hosts/${hostId}/containers/${cid}/inspect`),
  containerAction: (hostId: string, cid: string, action: ContainerAction, name?: string) =>
    request<void>("POST", `/hosts/${hostId}/containers/${cid}/actions/${action}`, { name }),
  removeContainer: (hostId: string, cid: string, o: { force: boolean; volumes: boolean; name?: string }) =>
    request<void>(
      "DELETE",
      `/hosts/${hostId}/containers/${cid}?force=${o.force}&volumes=${o.volumes}&name=${encodeURIComponent(o.name ?? "")}`,
    ),
};

/** URL of a hub WebSocket endpoint on the current origin. */
export function wsUrl(path: string, params?: Record<string, string | number | boolean | undefined>): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params ?? {})) if (v !== undefined && v !== "") q.set(k, String(v));
  const qs = q.toString();
  return `${proto}//${location.host}/api${path}${qs ? "?" + qs : ""}`;
}
