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
 * Which comics the server will actually delete, mirroring the switch in
 * handleDeleteComic so a button that shows here is one the server honours.
 * A library or claimed comic is missing on purpose: its file lives under the
 * read-only library root, so dropping the row would only lose the tags and
 * reading position and then resurrect the comic on the next scan.
 */
export function deletable(comic: Comic, isAdmin: boolean): boolean {
  if (comic.source === "upload") return comic.ownedByMe || isAdmin;
  if (comic.source === "library-pdf" || comic.source === "library-archive") return isAdmin;
  return false;
}

export function DeleteComicButton({ comic }: { comic: Comic }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const converted = comic.source === "library-pdf" || comic.source === "library-archive";

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
      <TileButton label={`Delete ${comicLabel(comic)}`} onClick={() => setConfirming(true)}>
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
