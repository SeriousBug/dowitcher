import { createContext, use, useEffect, useState, type ReactNode } from "react";
import { wsClient } from "../api/ws";
import type { ImportJob, LibraryStatus, WSMessage } from "../api/generated";

/** Push-stream connection state, surfaced so the header can show a live light. */
export type ConnectionState = "connecting" | "open" | "closed";

interface LiveDataValue {
  /** What the scanner is doing right now. Null until the server first says. */
  library: LibraryStatus | null;
  /** Every import job the server currently knows about, newest first. */
  jobs: ImportJob[];
  connection: ConnectionState;
}

const LiveDataContext = createContext<LiveDataValue | null>(null);

export function LiveDataProvider({ children }: { children: ReactNode }) {
  const [library, setLibrary] = useState<LibraryStatus | null>(null);
  const [jobs, setJobs] = useState<ImportJob[]>([]);
  const [connection, setConnection] = useState<ConnectionState>("connecting");

  useEffect(() => {
    const onLibrary = (msg: WSMessage) => {
      if (msg.library) setLibrary(msg.library);
    };

    // The complete job set, sent on connect. Replacing rather than merging is
    // the whole point: a job the server has since forgotten must disappear from
    // the list, or its progress bar sits at 40% forever on a reconnecting page.
    const onJobs = (msg: WSMessage) => {
      setJobs(msg.jobs ?? []);
    };

    // A single job update, merged in place. Jobs arrive here before they ever
    // appear in a full set, so an unknown id is an insert, not an error.
    const onJob = (msg: WSMessage) => {
      const job = msg.job;
      if (!job) return;
      setJobs((prev) => {
        const idx = prev.findIndex((j) => j.id === job.id);
        if (idx === -1) return [job, ...prev];
        const next = [...prev];
        next[idx] = job;
        return next;
      });
    };

    const unsubs = [
      wsClient.subscribe("library", onLibrary),
      wsClient.subscribe("jobs", onJobs),
      wsClient.subscribe("job", onJob),
      wsClient.onStatus(setConnection),
    ];
    wsClient.connect();

    return () => {
      for (const off of unsubs) off();
      wsClient.close();
    };
  }, []);

  const value: LiveDataValue = { library, jobs, connection };
  return <LiveDataContext value={value}>{children}</LiveDataContext>;
}

export function useLiveData(): LiveDataValue {
  const ctx = use(LiveDataContext);
  if (!ctx) throw new Error("useLiveData must be used within LiveDataProvider");
  return ctx;
}
