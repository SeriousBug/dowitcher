import { useSyncExternalStore } from "react";
import { downloadsSnapshot, subscribeDownloads, type DownloadState } from "./downloads";

/**
 * The downloads manifest, live.
 *
 * Not a useQuery: this is local state that changes once per page fetched, and
 * a download is the one thing on screen whose progress bar has to move while
 * it happens. An external store gives every mounted view the same numbers
 * without a cache round trip per page.
 */
export function useDownloads(): ReadonlyMap<string, DownloadState> {
  return useSyncExternalStore(subscribeDownloads, downloadsSnapshot);
}

export function useDownload(comicID: string): DownloadState | undefined {
  return useDownloads().get(comicID);
}
