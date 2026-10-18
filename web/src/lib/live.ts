import { useEffect, useSyncExternalStore } from "react";
import type { QueryClient } from "@tanstack/react-query";
import type { Host, HostMetrics, LiveEvent } from "./types";

// Rolling window of recent live samples per host, for sparklines.
const HISTORY = 120;
const EMPTY: HostMetrics[] = [];
const history = new Map<string, HostMetrics[]>();
const historyListeners = new Set<() => void>();
let historyVersion = 0;

function push(hostId: string, m: HostMetrics) {
  const prev = history.get(hostId) ?? EMPTY;
  const next = prev.length >= HISTORY ? prev.slice(prev.length - HISTORY + 1) : prev.slice();
  next.push(m);
  history.set(hostId, next);
  historyVersion++;
  historyListeners.forEach((l) => l());
}

const subscribeHistory = (l: () => void) => {
  historyListeners.add(l);
  return () => {
    historyListeners.delete(l);
  };
};

export function useLiveHistory(hostId: string): HostMetrics[] {
  useSyncExternalStore(subscribeHistory, () => historyVersion);
  return history.get(hostId) ?? EMPTY;
}

export type ConnState = "connecting" | "open" | "closed";
let conn: ConnState = "closed";
const connListeners = new Set<() => void>();
const setConn = (s: ConnState) => {
  conn = s;
  connListeners.forEach((l) => l());
};
const subscribeConn = (l: () => void) => {
  connListeners.add(l);
  return () => {
    connListeners.delete(l);
  };
};
export const useLiveConnection = () => useSyncExternalStore(subscribeConn, () => conn);

function patchHost(qc: QueryClient, id: string, patch: (h: Host) => Host) {
  qc.setQueryData<Host[]>(["hosts"], (list) => list?.map((h) => (h.id === id ? patch(h) : h)));
  qc.setQueryData<Host>(["host", id], (h) => (h ? patch(h) : h));
}

const pending = new Map<string, number>();
function scheduleRefetch(qc: QueryClient, key: string[]) {
  const k = key.join("/");
  if (pending.has(k)) return;
  pending.set(
    k,
    window.setTimeout(() => {
      pending.delete(k);
      qc.invalidateQueries({ queryKey: key });
    }, 400),
  );
}

/** Keeps a WebSocket to the hub open while mounted and folds events into the query cache. */
export function useLiveEvents(qc: QueryClient, enabled: boolean) {
  useEffect(() => {
    if (!enabled) return;
    let ws: WebSocket | null = null;
    let stopped = false;
    let retry = 1000;
    let timer: number | undefined;

    const open = () => {
      setConn("connecting");
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      ws = new WebSocket(`${proto}//${location.host}/api/ws`);
      ws.onopen = () => {
        retry = 1000;
        setConn("open");
        // Catch up on anything missed while disconnected.
        qc.invalidateQueries({ queryKey: ["hosts"] });
      };
      ws.onmessage = (msg) => {
        const ev = JSON.parse(msg.data) as LiveEvent;
        switch (ev.type) {
          case "host.metrics":
            push(ev.host_id, ev.data);
            patchHost(qc, ev.host_id, (h) => ({ ...h, metrics: ev.data }));
            break;
          case "host.info":
            patchHost(qc, ev.host_id, (h) => ({ ...h, info: ev.data }));
            break;
          case "host.status":
            patchHost(qc, ev.host_id, (h) => ({
              ...h,
              status: ev.data.status,
              error: ev.data.error,
              status_since: ev.data.since,
              metrics: ev.data.status === "online" ? h.metrics : undefined,
            }));
            break;
          case "docker.event":
            // Bursts (compose up, prune) arrive as many events: refetch once.
            if (ev.data.type === "container") scheduleRefetch(qc, ["containers", ev.host_id]);
            break;
        }
      };
      ws.onclose = () => {
        setConn("closed");
        if (stopped) return;
        timer = window.setTimeout(open, retry);
        retry = Math.min(retry * 2, 30000);
      };
    };
    open();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
      ws?.close();
    };
  }, [qc, enabled]);
}
