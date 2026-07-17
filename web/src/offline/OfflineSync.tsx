import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthProvider";
import { hydrateDownloads } from "./downloads";
import { onProgressReconciled, replayProgressQueue } from "./progressQueue";
import type { ComicDetail } from "../api/generated";

/**
 * The offline layer's one moving part at the app level: it reads the downloads
 * manifest into memory and drains the progress queue whenever draining it
 * might work.
 *
 * Renders nothing, and lives inside AuthProvider because replaying while
 * signed out would spend the queue on 401s.
 */
export function OfflineSync() {
  const { user } = useAuth();
  const queryClient = useQueryClient();

  useEffect(() => {
    void hydrateDownloads();
  }, []);

  useEffect(
    () =>
      onProgressReconciled((progress) => {
        // The reader may be open on this very comic. Patch rather than
        // invalidate: a refetch here would race the position the reader is
        // about to write next.
        queryClient.setQueryData<ComicDetail>(["comic", progress.comicId], (prev) =>
          prev ? { ...prev, progress } : prev,
        );
        queryClient.invalidateQueries({ queryKey: ["comics"] });
      }),
    [queryClient],
  );

  useEffect(() => {
    if (!user) return;
    // On launch as well as on reconnect: a tab can start up online with a queue
    // left over from a session that ended offline, and no `online` event will
    // ever fire to tell it so.
    void replayProgressQueue();
    const onOnline = () => void replayProgressQueue();
    window.addEventListener("online", onOnline);
    return () => window.removeEventListener("online", onOnline);
  }, [user]);

  return null;
}
