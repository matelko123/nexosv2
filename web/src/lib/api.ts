import type {
  AuditEntry,
  Container,
  ContainerAction,
  DockerImage,
  DockerNetwork,
  DockerVolume,
  Enrollment,
  Host,
  Me,
  MetricPoint,
  PruneResult,
  PullProgress,
  Stack,
  StackDetail,
  StackOperation,
  StreamMsg,
} from "./types";

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

/**
 * Calls an endpoint that answers with newline-delimited JSON, invoking
 * onMsg for each object as it arrives. Errors before the stream starts
 * reject with ApiError; errors inside the stream arrive as {error} objects.
 */
export async function streamRequest<T>(method: string, path: string, body: unknown, onMsg: (m: T) => void): Promise<void> {
  const headers: Record<string, string> = { "X-CSRF-Token": csrfToken };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch("/api" + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  if (!res.ok || !res.body) {
    const data = await res.json().catch(() => ({}));
    throw new ApiError(res.status, (data as { error?: string }).error ?? res.statusText);
  }
  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += value;
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (line) onMsg(JSON.parse(line) as T);
    }
  }
  if (buf.trim()) onMsg(JSON.parse(buf) as T);
}

export const resources = {
  images: (h: string) => request<DockerImage[]>("GET", `/hosts/${h}/images`),
  pullImage: (h: string, reference: string, on: (m: PullProgress) => void) => streamRequest("POST", `/hosts/${h}/images/pull`, { reference }, on),
  removeImage: (h: string, id: string, force: boolean, name?: string) =>
    request<{ deleted?: string[]; untagged?: string[] }>("DELETE", `/hosts/${h}/images/${id}?force=${force}&name=${encodeURIComponent(name ?? "")}`),
  pruneImages: (h: string, all: boolean) => request<PruneResult>("POST", `/hosts/${h}/images/prune`, { all }),

  networks: (h: string) => request<DockerNetwork[]>("GET", `/hosts/${h}/networks`),
  createNetwork: (h: string, b: { name: string; driver: string; internal: boolean; attachable: boolean; ipv6: boolean; subnet: string; gateway: string }) =>
    request<{ id: string; warnings?: string[] }>("POST", `/hosts/${h}/networks`, b),
  removeNetwork: (h: string, id: string, name?: string) => request<void>("DELETE", `/hosts/${h}/networks/${id}?name=${encodeURIComponent(name ?? "")}`),
  connectNetwork: (h: string, id: string, container: string, aliases: string[]) =>
    request<void>("POST", `/hosts/${h}/networks/${id}/connect`, { container, aliases }),
  disconnectNetwork: (h: string, id: string, container: string) => request<void>("POST", `/hosts/${h}/networks/${id}/disconnect`, { container, force: false }),
  pruneNetworks: (h: string) => request<PruneResult>("POST", `/hosts/${h}/networks/prune`),

  volumes: (h: string) => request<DockerVolume[]>("GET", `/hosts/${h}/volumes`),
  createVolume: (h: string, name: string) => request<DockerVolume>("POST", `/hosts/${h}/volumes`, { name }),
  removeVolume: (h: string, name: string) => request<void>("DELETE", `/hosts/${h}/volumes/${name}`),
  pruneVolumes: (h: string, all: boolean) => request<PruneResult>("POST", `/hosts/${h}/volumes/prune`, { all }),

  stacks: (h: string) => request<Stack[]>("GET", `/hosts/${h}/stacks`),
  stack: (h: string, name: string) => request<StackDetail>("GET", `/hosts/${h}/stacks/${name}`),
  createStack: (h: string, b: { name: string; compose: string; env: string }) => request<Stack>("POST", `/hosts/${h}/stacks`, b),
  updateStack: (h: string, name: string, b: { compose: string; env: string }) => request<Stack>("PUT", `/hosts/${h}/stacks/${name}`, b),
  runStack: (h: string, name: string, b: { operation: StackOperation; pull?: boolean; remove_volumes?: boolean }, on: (m: StreamMsg) => void) =>
    streamRequest("POST", `/hosts/${h}/stacks/${name}/run`, b, on),
  deleteStack: (h: string, name: string, down: boolean, volumes: boolean, on: (m: StreamMsg) => void) =>
    streamRequest("DELETE", `/hosts/${h}/stacks/${name}?down=${down}&volumes=${volumes}`, undefined, on),
};

/** URL of a hub WebSocket endpoint on the current origin. */
export function wsUrl(path: string, params?: Record<string, string | number | boolean | undefined>): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params ?? {})) if (v !== undefined && v !== "") q.set(k, String(v));
  const qs = q.toString();
  return `${proto}//${location.host}/api${path}${qs ? "?" + qs : ""}`;
}
