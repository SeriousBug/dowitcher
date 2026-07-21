import { useEffect, useState } from "react";
import { Dialog, Portal } from "@ark-ui/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { Button } from "./Button";
import type { Comic } from "../api/generated";

/**
 * Renaming sets the comic's display title. For a library comic the title comes
 * from the file, and this override wins over it and survives rescans — the
 * server keeps the two apart so a rename is not undone the next time the folder
 * is walked.
 */
export function RenameDialog({
  comic,
  open,
  onOpenChange,
}: {
  comic: Comic | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [title, setTitle] = useState("");

  // Reopening on a different comic starts from that comic's current title.
  useEffect(() => {
    if (open && comic) setTitle(comic.title);
  }, [open, comic]);

  const save = useMutation({
    mutationFn: (next: string) =>
      http.patch<Comic>(`/api/comics/${comic!.id}`, { title: next }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["comics"] });
      if (comic) queryClient.invalidateQueries({ queryKey: ["comic", comic.id] });
      toaster.create({ type: "success", title: "Renamed" });
      onOpenChange(false);
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: "Couldn't rename that",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
    },
  });

  const trimmed = title.trim();

  return (
    <Dialog.Root open={open} onOpenChange={(d) => onOpenChange(d.open)} lazyMount unmountOnExit>
      <Portal>
        <Dialog.Backdrop
          className={css({
            position: "fixed",
            inset: "0",
            bg: "rgba(10, 8, 9, 0.7)",
            backdropFilter: "blur(3px)",
            zIndex: "50",
          })}
        />
        <Dialog.Positioner
          className={css({
            position: "fixed",
            inset: "0",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            p: "4",
            zIndex: "50",
          })}
        >
          <Dialog.Content
            className={vstack({
              gap: "5",
              alignItems: "stretch",
              w: "full",
              maxW: "md",
              p: "6",
              bg: "surfaceRaised",
              borderWidth: "1px",
              borderColor: "border",
              borderRadius: "xl",
              boxShadow: "pop",
            })}
          >
            <Dialog.Title className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}>
              Rename comic
            </Dialog.Title>

            <label className={vstack({ gap: "1.5", alignItems: "stretch" })}>
              <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
                Title
              </span>
              <input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && trimmed) save.mutate(trimmed);
                }}
                autoFocus
                className={css({
                  w: "full",
                  px: "3.5",
                  py: "2.5",
                  borderRadius: "md",
                  borderWidth: "1px",
                  borderColor: "border",
                  bg: "bg",
                  color: "text",
                  fontSize: "sm",
                  _placeholder: { color: "ink.500" },
                  _focus: { outline: "none", borderColor: "accent" },
                })}
              />
            </label>

            <div className={hstack({ gap: "3", justify: "flex-end" })}>
              <Dialog.CloseTrigger asChild>
                <Button variant="ghost">Never mind</Button>
              </Dialog.CloseTrigger>
              <Button
                variant="primary"
                busy={save.isPending}
                disabled={!trimmed}
                icon={<Check size={16} />}
                onClick={() => save.mutate(trimmed)}
              >
                Save
              </Button>
            </div>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}
