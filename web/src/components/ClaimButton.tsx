import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { FolderInput, FolderOutput } from "lucide-react";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { comicLabel } from "../lib/format";
import { ConfirmDialog } from "./ConfirmDialog";
import { TileButton } from "./ComicGrid";
import type { Comic } from "../api/generated";

/**
 * Claiming moves a comic out of every other user's library, so it is confirmed
 * rather than fired off a hover button: the tile it is on gives no hint of who
 * else is currently reading the thing.
 *
 * Only rendered for admins, and only on the two states the action is defined
 * for — a library comic to claim, or a claim of the caller's own to undo. An
 * upload has an owner already and is neither.
 */
export function ClaimButton({ comic }: { comic: Comic }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const claimed = comic.source === "claimed";

  const act = useMutation({
    mutationFn: () => http.post<{ ok: boolean }>(`/api/comics/${comic.id}/${claimed ? "unclaim" : "claim"}`),
    onSuccess: () => {
      // The comic either left or rejoined the server-wide library, so every
      // listing, the comic's own source, and the tag counts alongside them are
      // now wrong.
      queryClient.invalidateQueries({ queryKey: ["comics"] });
      queryClient.invalidateQueries({ queryKey: ["comic", comic.id] });
      queryClient.invalidateQueries({ queryKey: ["tags"] });
      toaster.create({
        type: "success",
        title: claimed ? "Back in the shared library" : "Claimed",
        description: claimed
          ? `Everyone on this Dowitcher can read ${comicLabel(comic)} again.`
          : `${comicLabel(comic)} is yours alone now. The file stays where it is.`,
      });
      setConfirming(false);
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: claimed ? "Couldn't give that back" : "Couldn't claim that",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
      setConfirming(false);
    },
  });

  const label = claimed
    ? `Return ${comicLabel(comic)} to the shared library`
    : `Claim ${comicLabel(comic)} into your library`;

  return (
    <>
      <TileButton label={label} onClick={() => setConfirming(true)}>
        {claimed ? <FolderOutput size={14} /> : <FolderInput size={14} />}
      </TileButton>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={claimed ? "Return this to everyone?" : "Claim this comic?"}
        description={
          claimed
            ? `${comicLabel(comic)} goes back to being part of the shared library, and everyone on this server will see it again. Your tags and reading position stay put.`
            : `${comicLabel(comic)} moves into your library and disappears from everyone else's. The file stays in the library folder, and you can hand it back at any time.`
        }
        confirmLabel={claimed ? "Give it back" : "Claim it"}
        onConfirm={() => act.mutate()}
      />
    </>
  );
}
