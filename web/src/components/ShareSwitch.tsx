import { Switch } from "@ark-ui/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Lock, Users } from "lucide-react";
import { css } from "styled-system/css";
import { hstack } from "styled-system/patterns";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import type { Collection } from "../api/generated";

/**
 * Sharing is the one decision on a collection that other people can see, so it
 * gets a control that states its own answer in words rather than a bare switch
 * you have to read the position of.
 */
export function ShareSwitch({ collection }: { collection: Collection }) {
  const queryClient = useQueryClient();

  const toggle = useMutation({
    mutationFn: (shared: boolean) =>
      http.put<Collection>(`/api/collections/${collection.id}`, { shared }),
    onSuccess: (_data, shared) => {
      queryClient.invalidateQueries({ queryKey: ["collections"] });
      queryClient.invalidateQueries({ queryKey: ["collection", collection.id] });
      toaster.create({
        type: "success",
        title: shared ? "Shared" : "Back to private",
        description: shared
          ? `Everyone on this Longbox can now read ${collection.name}.`
          : `${collection.name} is yours alone again.`,
      });
    },
    onError: (err, shared) => {
      toaster.create({
        type: "error",
        title: shared ? "Couldn't share that" : "Couldn't unshare that",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
    },
  });

  return (
    <Switch.Root
      checked={collection.shared}
      disabled={toggle.isPending}
      onCheckedChange={(d) => toggle.mutate(d.checked)}
      className={hstack({
        gap: "2.5",
        cursor: "pointer",
        _disabled: { opacity: 0.6, cursor: "wait" },
      })}
    >
      <Switch.Control
        className={css({
          position: "relative",
          w: "9",
          h: "5",
          p: "0.5",
          borderRadius: "full",
          bg: "ink.750",
          borderWidth: "1px",
          borderColor: "border",
          flexShrink: 0,
          transition: "background 0.15s ease, border-color 0.15s ease",
          "&[data-state='checked']": { bg: "accent", borderColor: "accent" },
        })}
      >
        <Switch.Thumb
          className={css({
            display: "block",
            w: "3.5",
            h: "3.5",
            borderRadius: "full",
            bg: "ink.300",
            transition: "transform 0.15s ease, background 0.15s ease",
            "&[data-state='checked']": { transform: "translateX(16px)", bg: "white" },
          })}
        />
      </Switch.Control>
      <Switch.Label
        className={hstack({
          gap: "1.5",
          fontSize: "xs",
          fontWeight: "bold",
          color: collection.shared ? "magenta.300" : "textMuted",
        })}
      >
        {collection.shared ? <Users size={12} /> : <Lock size={12} />}
        {collection.shared ? "Shared" : "Private"}
      </Switch.Label>
      <Switch.HiddenInput />
    </Switch.Root>
  );
}
