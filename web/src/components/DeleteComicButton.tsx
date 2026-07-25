import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { comicLabel } from "../lib/format";
import { ConfirmDialog } from "./ConfirmDialog";
import { TileButton } from "./ComicGrid";
import type { Comic } from "../api/generated";

/**
 * Why the server would refuse to delete this comic, or undefined if it would
 * allow it. Mirrors store.CanDeleteComic so the button's enabled state matches
 * what the request would actually do.
 *
 * The button stays on the tile either way, greyed out with the reason as its
 * tooltip: "why is there no delete button on this one" is the question the
 * read-only library root provokes, and a missing button cannot answer it.
 */
export function deleteRefusal(comic: Comic, isAdmin: boolean): string | undefined {
  if (comic.source === "upload") {
    return comic.ownedByMe || isAdmin ? undefined : "Only the uploader can delete this upload";
  }
  if (comic.source === "library-pdf" || comic.source === "library-archive") {
    return isAdmin ? undefined : "Only an admin can delete a server-wide comic";
  }
  return "In the library folder, so it has to be deleted there — the folder is read-only to Dowitcher";
}

export function DeleteComicButton({ comic, isAdmin }: { comic: Comic; isAdmin: boolean }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const converted = comic.source === "library-pdf" || comic.source === "library-archive";
  const refusal = deleteRefusal(comic, isAdmin);

  const act = useMutation({
    mutationFn: () => http.del<{ ok: boolean }>(`/api/comics/${comic.id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["comics"] });
      queryClient.invalidateQueries({ queryKey: ["comic", comic.id] });
      // Tags cascade with the comic, so the counts behind the tag list moved too.
      queryClient.invalidateQueries({ queryKey: ["tags"] });
      queryClient.invalidateQueries({ queryKey: ["collections"] });
      toaster.create({
        type: "success",
        title: "Deleted",
        description: `${comicLabel(comic)} and its file are gone.`,
      });
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: "Couldn't delete that",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
    },
  });

  return (
    <>
      <TileButton
        label={`Delete ${comicLabel(comic)}`}
        disabled={refusal}
        onClick={() => setConfirming(true)}
      >
        <Trash2 size={14} />
      </TileButton>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Delete this comic?"
        tone="danger"
        description={
          converted
            ? `${comicLabel(comic)} is deleted for good, along with its CBZ, everyone's tags on it, and everyone's reading position. The file it was converted from stays in the library folder, but it will not be converted again.`
            : `${comicLabel(comic)} is deleted for good, along with its CBZ, everyone's tags on it, and everyone's reading position.`
        }
        confirmLabel="Delete it"
        onConfirm={() => act.mutate()}
      />
    </>
  );
}
