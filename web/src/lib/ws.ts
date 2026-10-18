import { useEffect, useRef, useState } from "react";

export type SocketState = "connecting" | "open" | "closed" | "error";

/**
 * Opens a WebSocket to `url` while mounted (and while url is non-null),
 * delivering parsed JSON text messages to onMessage. Reconnects with backoff
 * unless the server ended the stream normally.
 */
export function useJsonSocket<T>(url: string | null, onMessage: (msg: T) => void, opts: { reconnect?: boolean } = {}) {
  const [state, setState] = useState<SocketState>("closed");
  const handler = useRef(onMessage);
  handler.current = onMessage;
  const reconnect = opts.reconnect ?? true;

  useEffect(() => {
    if (!url) return;
    let ws: WebSocket | null = null;
    let stopped = false;
    let retry = 1000;
    let timer: number | undefined;
    const open = () => {
      setState("connecting");
      ws = new WebSocket(url);
      ws.onopen = () => {
        retry = 1000;
        setState("open");
      };
      ws.onmessage = (e) => {
        if (typeof e.data === "string") handler.current(JSON.parse(e.data) as T);
      };
      ws.onclose = (e) => {
        if (stopped) return;
        setState(e.code === 1000 ? "closed" : "error");
        if (reconnect && e.code !== 1000) {
          timer = window.setTimeout(open, retry);
          retry = Math.min(retry * 2, 15000);
        }
      };
    };
    open();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
      ws?.close();
    };
  }, [url, reconnect]);

  return state;
}
