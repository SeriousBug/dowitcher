import { useEffect, useMemo, useState } from "react";
import { Combobox, Dialog, Portal, useListCollection } from "@ark-ui/react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Tag as TagIcon, X } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { comicLabel } from "../lib/format";
import { Button } from "./Button";
import type { Comic, Tag } from "../api/generated";

/**
 * Tags are server-global, so the vocabulary already in use is the useful thing to
 * offer: autocomplete against it first and let someone type a new one only when
 * nothing existing fits. A free-text field would grow four spellings of "sci-fi"
 * inside a week.
 */
export function TagEditorDialog({
  comic,
  open,
  onOpenChange,
}: {
  comic: Comic | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<string[]>([]);

  const tagsQuery = useQuery({
    queryKey: ["tags"],
    queryFn: () => http.get<Tag[]>("/api/tags"),
    enabled: open,
  });

  // Reopening on a different comic must start from that comic's tags, not from
  // whatever was left selected last time.
  useEffect(() => {
    if (open && comic) setSelected(comic.tags ?? []);
  }, [open, comic]);

  const names = useMemo(() => (tagsQuery.data ?? []).map((t) => t.name), [tagsQuery.data]);

  const save = useMutation({
    mutationFn: (tags: string[]) =>
      http.put<{ ok: boolean }>(`/api/comics/${comic!.id}/tags`, { tags }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["comics"] });
      queryClient.invalidateQueries({ queryKey: ["tags"] });
      toaster.create({ type: "success", title: "Tags saved" });
      onOpenChange(false);
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: "Couldn't save those tags",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
    },
  });

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
            <div className={vstack({ gap: "1.5", alignItems: "stretch" })}>
              <Dialog.Title
                className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}
              >
                Tags
              </Dialog.Title>
              <Dialog.Description className={css({ color: "textMuted", fontSize: "sm" })}>
                {comic ? comicLabel(comic) : ""}
              </Dialog.Description>
            </div>

            <TagPicker
              all={names}
              selected={selected}
              onChange={setSelected}
              loading={tagsQuery.isLoading}
            />

            <div className={hstack({ gap: "3", justify: "flex-end" })}>
              <Dialog.CloseTrigger asChild>
                <Button variant="ghost">Never mind</Button>
              </Dialog.CloseTrigger>
              <Button
                variant="primary"
                busy={save.isPending}
                icon={<Check size={16} />}
                onClick={() => save.mutate(selected)}
              >
                Save tags
              </Button>
            </div>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}

/**
 * Multi-select over the tags already on the server, with a free-typed tag
 * allowed as the escape hatch. Split out from the dialog so a comic detail pane
 * can reuse it without a modal around it.
 */
export function TagPicker({
  all,
  selected,
  onChange,
  loading = false,
}: {
  all: string[];
  selected: string[];
  onChange: (tags: string[]) => void;
  loading?: boolean;
}) {
  const { collection, filter, set } = useListCollection<string>({
    initialItems: all,
    filter: (itemText, filterText) => itemText.toLowerCase().includes(filterText.toLowerCase()),
  });

  // The tag list arrives after this mounts, and useListCollection only reads
  // initialItems once.
  useEffect(() => set(all), [all, set]);

  return (
    <div className={vstack({ gap: "3", alignItems: "stretch" })}>
      <Combobox.Root
        collection={collection}
        multiple
        allowCustomValue
        closeOnSelect={false}
        value={selected}
        onValueChange={(d) => onChange(d.value)}
        onInputValueChange={(d) => filter(d.inputValue)}
        className={vstack({ gap: "2", alignItems: "stretch" })}
      >
        <Combobox.Label className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
          Add a tag
        </Combobox.Label>
        <Combobox.Control
          className={hstack({
            gap: "2.5",
            px: "3.5",
            py: "2.5",
            borderRadius: "md",
            bg: "bg",
            borderWidth: "1px",
            borderColor: "border",
            _focusWithin: { borderColor: "accent" },
          })}
        >
          <TagIcon size={15} className={css({ color: "ink.500", flexShrink: 0 })} />
          <Combobox.Input
            placeholder={loading ? "Loading tags…" : "Type to search or invent one"}
            className={css({
              flex: "1",
              minW: "0",
              bg: "transparent",
              color: "text",
              fontSize: "sm",
              _placeholder: { color: "ink.500" },
              _focus: { outline: "none" },
            })}
          />
        </Combobox.Control>
        <Portal>
          <Combobox.Positioner className={css({ zIndex: "60" })}>
            <Combobox.Content
              className={vstack({
                gap: "0.5",
                alignItems: "stretch",
                w: "full",
                maxH: "56",
                overflowY: "auto",
                p: "1.5",
                bg: "surfaceRaised",
                borderWidth: "1px",
                borderColor: "border",
                borderRadius: "lg",
                boxShadow: "pop",
              })}
            >
              <Combobox.Empty
                className={css({ px: "3", py: "2.5", fontSize: "sm", color: "textMuted" })}
              >
                No tag by that name yet — press Enter to make it.
              </Combobox.Empty>
              {collection.items.map((name) => (
                <Combobox.Item
                  key={name}
                  item={name}
                  className={hstack({
                    gap: "2",
                    justify: "space-between",
                    px: "3",
                    py: "2",
                    borderRadius: "md",
                    fontSize: "sm",
                    color: "text",
                    cursor: "pointer",
                    "&[data-highlighted]": { bg: "accentQuiet" },
                    "&[data-state='checked']": { color: "magenta.300" },
                  })}
                >
                  <Combobox.ItemText>{name}</Combobox.ItemText>
                  <Combobox.ItemIndicator>
                    <Check size={14} />
                  </Combobox.ItemIndicator>
                </Combobox.Item>
              ))}
            </Combobox.Content>
          </Combobox.Positioner>
        </Portal>
      </Combobox.Root>

      {selected.length > 0 && (
        <div className={hstack({ gap: "2", flexWrap: "wrap" })}>
          {selected.map((tag) => (
            <span
              key={tag}
              className={hstack({
                gap: "1.5",
                pl: "3",
                pr: "1.5",
                py: "1",
                borderRadius: "full",
                bg: "accentQuiet",
                borderWidth: "1px",
                borderColor: "magenta.900",
                color: "magenta.300",
                fontSize: "xs",
                fontWeight: "semibold",
              })}
            >
              {tag}
              <button
                onClick={() => onChange(selected.filter((t) => t !== tag))}
                aria-label={`Remove ${tag}`}
                title={`Remove ${tag}`}
                className={css({
                  display: "flex",
                  p: "0.5",
                  borderRadius: "full",
                  color: "magenta.300",
                  cursor: "pointer",
                  _hover: { bg: "magenta.900", color: "text" },
                })}
              >
                <X size={12} />
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
