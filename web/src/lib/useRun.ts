import { useCallback, useState } from "react";
import type { RunState } from "../components/OutputConsole";
import type { StreamMsg } from "./types";

/**
 * State for one streamed operation at a time. `start` runs `fn`, feeding
 * every message into the output and resolving with the final state.
 */
export function useRun() {
  const [run, setRun] = useState<RunState | null>(null);
  const start = useCallback(async (title: string, fn: (on: (m: StreamMsg) => void) => Promise<void>) => {
    let state: RunState = { title, lines: [], running: true };
    setRun(state);
    const update = (patch: Partial<RunState>) => {
      state = { ...state, ...patch };
      setRun(state);
    };
    try {
      await fn((m) => {
        if (m.line !== undefined) update({ lines: [...state.lines, m.line] });
        if (m.exit_code !== undefined) update({ exitCode: m.exit_code });
        if (m.error) update({ error: m.error });
      });
      update({ running: false });
    } catch (e) {
      update({ running: false, error: e instanceof Error ? e.message : String(e) });
    }
    return state;
  }, []);
  return { run, start, clear: () => setRun(null) };
}
