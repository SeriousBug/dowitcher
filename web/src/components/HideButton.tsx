import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { EyeOff } from "lucide-react";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { comicLabel } from "../lib/format";
import { ConfirmDialog } from "./ConfirmDialog";
import { TileButton } from "./ComicGrid";
import type { Comic } from "../api/generated";

/**
 * Hiding is the soft delete, and the answer to a comic delete refuses: a library
 * CBZ sits under a read-only root, so taking it off the shelf is the only thing
 * dowitcher can do to it. Admin-only, because it takes the comic off everyone's
 * shelf and not just the clicker's.
 */
export function HideButton({ comic }: { comic: Comic }) {
  const queryClient = useQueryClient();
  const [confirming, setConfirming] = useState(false);

  const act = useMutation({
    mutationFn: () => http.post<{ ok: boolean }>(`/api/comics/${comic.id}/hide`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["comics"] });
      queryClient.invalidateQueries({ queryKey: ["comic", comic.id] });
      queryClient.invalidateQueries({ queryKey: ["hidden-comics"] });
      // Tag counts move with it, since a hidden comic stops matching any tag.
      queryClient.invalidateQueries({ queryKey: ["tags"] });
      toaster.create({
        type: "success",
        title: "Hidden",
        description: `${comicLabel(comic)} is off every shelf. Settings has the list, to put it back.`,
      });
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: "Couldn't hide that",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
    },
  });

  return (
    <>
      <TileButton label={`Hide ${comicLabel(comic)}`} onClick={() => setConfirming(true)}>
        <EyeOff size={14} />
      </TileButton>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Hide this comic?"
        confirmLabel="Hide it"
        onConfirm={() => act.mutate()}
        description={`${comicLabel(comic)} comes off the shelf for everyone on this server. Nothing is deleted — the file, the tags and everyone's reading position all stay, and you can put it back from Settings.`}
      />
    </>
  );
}
