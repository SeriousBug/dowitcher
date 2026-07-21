import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Dialog, Portal } from "@ark-ui/react";
import {
  ArrowLeft,
  BookPlus,
  ChevronLeft,
  ChevronRight,
  Eye,
  GripVertical,
  Image as ImageIcon,
  Pencil,
  Search,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ComicGrid, ComicGridSkeleton, ComicTile, TileButton } from "../components/ComicGrid";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DropOverlay } from "../components/DropOverlay";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { ShareSwitch } from "../components/ShareSwitch";
import { useAuth } from "../auth/AuthProvider";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { fetchComics } from "../api/comics";
import { comicLabel } from "../lib/format";
import { KIND_CONFIG, type CollectionKind } from "../lib/collectionKind";
import type { Collection, Comic } from "../api/generated";

/** Every mutation here reports the same way, so they say it in one place. */
function failed(title: string) {
  return (err: unknown) =>
    toaster.create({
      type: "error",
      title,
      description:
        err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
    });
}

export function CollectionDetailPage({
  id,
  kind = "collection",
}: {
  id: string;
  kind?: CollectionKind;
}) {
  const cfg = KIND_CONFIG[kind];
  const { user } = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [adding, setAdding] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const collectionQuery = useQuery({
    queryKey: ["collection", id],
    queryFn: () => http.get<Collection>(`/api/collections/${id}`),
    retry: false,
  });

  const comicsQuery = useQuery({
    queryKey: ["comics", { collection: id }],
    // A collection is ordered, so the server's order is the order — nothing here
    // re-sorts it.
    queryFn: () => fetchComics({ collection: id }, 0, 500),
  });

  const collection = collectionQuery.data;
  const owned = Boolean(collection && user && collection.ownerId === user.id);
  const comics = comicsQuery.data?.comics ?? [];

  // A local copy of the order so a drag reorders in place without waiting on the
  // round trip. It resyncs from the server only when the *set* of comics changes
  // (one added or removed), so an in-flight reorder is not clobbered by the
  // refetch it triggers.
  const [order, setOrder] = useState<Comic[]>(comics);
  useEffect(() => {
    setOrder((prev) => {
      const ids = comics.map((c) => c.id);
      const prevIds = prev.map((c) => c.id);
      const sameSet =
        ids.length === prevIds.length && ids.every((cid) => prevIds.includes(cid));
      if (sameSet) return prev.map((p) => comics.find((c) => c.id === p.id) ?? p);
      return comics;
    });
  }, [comics]);
  const [dragIndex, setDragIndex] = useState<number | null>(null);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["comics", { collection: id }] });
    queryClient.invalidateQueries({ queryKey: ["collection", id] });
    queryClient.invalidateQueries({ queryKey: ["collections"] });
  };

  const removeCollection = useMutation({
    mutationFn: () => http.del<{ ok: boolean }>(`/api/collections/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["collections"] });
      toaster.create({ type: "success", title: `Deleted ${collection?.name ?? cfg.singular}` });
      navigate({ to: cfg.basePath });
    },
    onError: failed(`Couldn't delete that ${cfg.singular}`),
  });

  const removeComic = useMutation({
    mutationFn: (comic: Comic) =>
      http.del<{ ok: boolean }>(`/api/collections/${id}/comics/${comic.id}`),
    onSuccess: (_data, comic) => {
      invalidate();
      toaster.create({ type: "success", title: `Took ${comicLabel(comic)} out` });
    },
    onError: failed("Couldn't remove that comic"),
  });

  const setCover = useMutation({
    mutationFn: (comic: Comic) =>
      http.put<Collection>(`/api/collections/${id}/cover`, { comicId: comic.id }),
    onSuccess: (_data, comic) => {
      invalidate();
      toaster.create({ type: "success", title: `${comicLabel(comic)} is now the cover` });
    },
    onError: failed("Couldn't set that cover"),
  });

  // The whole order goes up, not a move instruction: the server keeps positions
  // dense from the full list, which makes a retry after a dropped response
  // idempotent instead of a scramble.
  const reorder = useMutation({
    mutationFn: (comicIds: string[]) =>
      http.put<{ ok: boolean }>(`/api/collections/${id}/order`, { comicIds }),
    onSuccess: () => invalidate(),
    onError: failed("Couldn't save that order"),
  });

  function commitOrder(next: Comic[]) {
    setOrder(next);
    reorder.mutate(next.map((c) => c.id));
  }

  // Keyboard- and touch-reachable reorder: HTML5 drag does not fire on touch, so
  // the arrows stay as the accessible path alongside the drag handle.
  function move(index: number, by: number) {
    const to = index + by;
    if (to < 0 || to >= order.length) return;
    const next = [...order];
    [next[index], next[to]] = [next[to], next[index]];
    commitOrder(next);
  }

  // Live reorder as the pointer moves over another tile: the dragged item slots
  // into the tile it is over, and dragIndex follows it, so the grid rearranges
  // under the cursor instead of only on drop.
  function onDragEnter(i: number) {
    if (dragIndex === null || dragIndex === i) return;
    const next = [...order];
    const [moved] = next.splice(dragIndex, 1);
    next.splice(i, 0, moved);
    setOrder(next);
    setDragIndex(i);
  }

  function onDrop() {
    if (dragIndex === null) return;
    setDragIndex(null);
    reorder.mutate(order.map((c) => c.id));
  }

  if (collectionQuery.isLoading) {
    return (
      <div className={vstack({ gap: "7", alignItems: "stretch" })}>
        <BackLink kind={kind} />
        <ComicGridSkeleton />
      </div>
    );
  }

  if (!collection) {
    return (
      <div className={vstack({ gap: "6", alignItems: "stretch" })}>
        <BackLink kind={kind} />
        <EmptyState icon={Users} title={`This ${cfg.singular} isn't here`}>
          It may have been deleted, or its owner stopped sharing it. Reference:{" "}
          <code className={css({ fontFamily: "mono" })}>{id}</code>
        </EmptyState>
      </div>
    );
  }

  return (
    <DropOverlay collectionId={id} disabled={!owned}>
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <BackLink kind={kind} />

      <PageHeader
        eyebrow={cfg.singularCap}
        title={collection.name}
        subtitle={
          collection.summary ||
          (collection.count === 1 ? "1 comic" : `${collection.count} comics`)
        }
        actions={
          owned ? (
            <>
              <Button icon={<BookPlus size={16} />} onClick={() => setAdding(true)}>
                Add comics
              </Button>
              <Button
                icon={<Pencil size={16} />}
                aria-label={`Edit ${cfg.singular}`}
                title={`Edit ${cfg.singular}`}
                onClick={() => setEditing(true)}
              />
              <Button
                variant="ghost"
                icon={<Trash2 size={16} />}
                aria-label={`Delete ${cfg.singular}`}
                title={`Delete ${cfg.singular}`}
                onClick={() => setConfirmDelete(true)}
              />
            </>
          ) : undefined
        }
      />

      {/* Who can see this, stated on the page rather than inferred from a badge.
          On someone else's collection the same strip says why there are no
          buttons above it. */}
      <div
        className={hstack({
          gap: "3",
          justify: "space-between",
          flexWrap: "wrap",
          px: "4",
          py: "3",
          borderRadius: "lg",
          bg: "surface",
          borderWidth: "1px",
          borderColor: collection.shared ? "magenta.900" : "border",
          borderLeftWidth: "3px",
          borderLeftColor: collection.shared ? "accent" : "ink.700",
        })}
      >
        {owned ? (
          <>
            <ShareSwitch collection={collection} />
            <span className={css({ fontSize: "xs", color: "textMuted" })}>
              {collection.shared
                ? "Everyone with an account here can read this."
                : "Only you can see this one."}
            </span>
          </>
        ) : (
          <span className={hstack({ gap: "2", fontSize: "xs", color: "textMuted" })}>
            <Eye size={13} />
            Shared with you by {collection.ownerName ?? "someone else"} — you can read
            it, but only they can change it.
          </span>
        )}
      </div>

      {comicsQuery.isLoading ? (
        <ComicGridSkeleton count={6} />
      ) : order.length === 0 ? (
        <EmptyState
          icon={BookPlus}
          title="Nothing in here yet"
          action={
            owned ? (
              <Button variant="primary" icon={<BookPlus size={16} />} onClick={() => setAdding(true)}>
                Add comics
              </Button>
            ) : undefined
          }
        >
          {owned
            ? "Add comics from your library and they'll line up here in the order you put them."
            : "Its owner hasn't put anything in it yet."}
        </EmptyState>
      ) : (
        <ComicGrid>
          {order.map((comic, i) => {
            const isCover = collection.coverComicId === comic.id;
            const dragging = dragIndex === i;
            return (
              <div
                key={comic.id}
                onDragEnter={owned ? () => onDragEnter(i) : undefined}
                onDragOver={owned ? (e) => e.preventDefault() : undefined}
                onDrop={owned ? (e) => { e.preventDefault(); onDrop(); } : undefined}
                className={css({ opacity: dragging ? 0.4 : 1, transition: "opacity 0.15s ease" })}
              >
                <ComicTile
                  comic={comic}
                  progress={comicsQuery.data?.progress.get(comic.id)}
                  actions={
                    owned ? (
                      <>
                        <span
                          draggable
                          onDragStart={() => setDragIndex(i)}
                          onDragEnd={() => setDragIndex(null)}
                          aria-hidden
                          title="Drag to reorder"
                          className={css({
                            display: "flex",
                            alignItems: "center",
                            justifyContent: "center",
                            w: "7",
                            h: "7",
                            borderRadius: "sm",
                            bg: "rgba(10, 8, 9, 0.82)",
                            color: "ink.100",
                            cursor: "grab",
                            backdropFilter: "blur(4px)",
                            _active: { cursor: "grabbing" },
                            _hover: { bg: "accent", color: "white" },
                          })}
                        >
                          <GripVertical size={14} />
                        </span>
                        <TileButton
                          label={`Move ${comicLabel(comic)} earlier`}
                          onClick={() => move(i, -1)}
                        >
                          <ChevronLeft size={14} />
                        </TileButton>
                        <TileButton
                          label={`Move ${comicLabel(comic)} later`}
                          onClick={() => move(i, 1)}
                        >
                          <ChevronRight size={14} />
                        </TileButton>
                        <TileButton
                          label={
                            isCover
                              ? `${comicLabel(comic)} is the cover`
                              : `Use ${comicLabel(comic)} as the cover`
                          }
                          onClick={() => !isCover && setCover.mutate(comic)}
                        >
                          <ImageIcon
                            size={14}
                            className={isCover ? css({ color: "accent" }) : undefined}
                          />
                        </TileButton>
                        <TileButton
                          label={`Take ${comicLabel(comic)} out of this ${cfg.singular}`}
                          onClick={() => removeComic.mutate(comic)}
                        >
                          <X size={14} />
                        </TileButton>
                      </>
                    ) : undefined
                  }
                />
              </div>
            );
          })}
        </ComicGrid>
      )}

      <EditCollectionDialog
        collection={collection}
        kind={kind}
        open={editing}
        onOpenChange={setEditing}
        onSaved={invalidate}
      />

      <AddComicsDialog
        collectionId={id}
        kind={kind}
        alreadyIn={new Set(comics.map((c) => c.id))}
        open={adding}
        onOpenChange={setAdding}
        onAdded={invalidate}
      />

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete this ${cfg.singular}?`}
        description={
          <>
            <strong>{collection.name}</strong> goes away for good. The comics in it
            stay on your shelf — only the grouping is lost.
          </>
        }
        confirmLabel="Delete"
        tone="danger"
        onConfirm={() => removeCollection.mutate()}
      />
    </div>
    </DropOverlay>
  );
}

function EditCollectionDialog({
  collection,
  kind,
  open,
  onOpenChange,
  onSaved,
}: {
  collection: Collection;
  kind: CollectionKind;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}) {
  const cfg = KIND_CONFIG[kind];
  const [name, setName] = useState(collection.name);
  const [summary, setSummary] = useState(collection.summary ?? "");

  useEffect(() => {
    if (open) {
      setName(collection.name);
      setSummary(collection.summary ?? "");
    }
  }, [open, collection]);

  const save = useMutation({
    mutationFn: () =>
      http.put<Collection>(`/api/collections/${collection.id}`, {
        name: name.trim(),
        summary: summary.trim(),
      }),
    onSuccess: () => {
      onSaved();
      toaster.create({ type: "success", title: "Saved" });
      onOpenChange(false);
    },
    onError: failed("Couldn't save that"),
  });

  return (
    <ModalShell open={open} onOpenChange={onOpenChange} title={`Edit ${cfg.singular}`} maxW="md">
      <label className={vstack({ gap: "1.5", alignItems: "stretch" })}>
        <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>Name</span>
        <input value={name} onChange={(e) => setName(e.target.value)} className={FIELD} />
      </label>

      <label className={vstack({ gap: "1.5", alignItems: "stretch" })}>
        <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
          What's in it
        </span>
        <input
          value={summary}
          onChange={(e) => setSummary(e.target.value)}
          placeholder="Optional"
          className={FIELD}
        />
      </label>

      <div className={hstack({ gap: "3", justify: "flex-end" })}>
        <Dialog.CloseTrigger asChild>
          <Button variant="ghost">Never mind</Button>
        </Dialog.CloseTrigger>
        <Button
          variant="primary"
          busy={save.isPending}
          disabled={!name.trim()}
          onClick={() => save.mutate()}
        >
          Save
        </Button>
      </div>
    </ModalShell>
  );
}

/**
 * Pick comics out of the library to file in here. Searching hits the same list
 * endpoint the library does, so a big shelf doesn't have to be pulled down whole
 * just to find one issue.
 */
function AddComicsDialog({
  collectionId,
  kind,
  alreadyIn,
  open,
  onOpenChange,
  onAdded,
}: {
  collectionId: string;
  kind: CollectionKind;
  alreadyIn: Set<string>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: () => void;
}) {
  const cfg = KIND_CONFIG[kind];
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");
  const [picked, setPicked] = useState<Set<string>>(new Set());

  useEffect(() => {
    const t = setTimeout(() => setDebounced(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);

  useEffect(() => {
    if (!open) {
      setQ("");
      setPicked(new Set());
    }
  }, [open]);

  const results = useQuery({
    queryKey: ["comics", { q: debounced, picker: true }],
    queryFn: () => fetchComics({ q: debounced || undefined }, 0, 40),
    enabled: open,
  });

  const candidates = useMemo(
    () => (results.data?.comics ?? []).filter((c) => !alreadyIn.has(c.id)),
    [results.data, alreadyIn],
  );

  const add = useMutation({
    // The endpoint files one comic per call, so a multi-pick is a run of them.
    // Sequential rather than parallel: they land in the order they were picked,
    // and a collection's order is the point of it.
    mutationFn: async () => {
      for (const comicId of picked) {
        await http.post<{ ok: boolean }>(`/api/collections/${collectionId}/comics`, { comicId });
      }
    },
    onSuccess: () => {
      onAdded();
      toaster.create({
        type: "success",
        title: picked.size === 1 ? "Added 1 comic" : `Added ${picked.size} comics`,
      });
      onOpenChange(false);
    },
    onError: failed("Couldn't add those"),
  });

  return (
    <ModalShell open={open} onOpenChange={onOpenChange} title="Add comics" maxW="2xl">
      <label
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
        <Search size={16} className={css({ color: "ink.500", flexShrink: 0 })} />
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search your library"
          aria-label="Search your library"
          autoFocus
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
      </label>

      <div className={css({ maxH: "sm", overflowY: "auto", mx: "-2", px: "2" })}>
        {results.isLoading ? (
          <p className={css({ py: "8", textAlign: "center", color: "textMuted", fontSize: "sm" })}>
            Looking…
          </p>
        ) : candidates.length === 0 ? (
          <p className={css({ py: "8", textAlign: "center", color: "textMuted", fontSize: "sm" })}>
            {q ? `Nothing left to add matching “${q}”.` : `Everything you can read is already in this ${cfg.singular}.`}
          </p>
        ) : (
          <div className={grid({ columns: { base: 3, sm: 4, md: 5 }, gap: "3" })}>
            {candidates.map((comic) => {
              const on = picked.has(comic.id);
              return (
                <button
                  key={comic.id}
                  onClick={() =>
                    setPicked((prev) => {
                      const next = new Set(prev);
                      if (on) next.delete(comic.id);
                      else next.add(comic.id);
                      return next;
                    })
                  }
                  aria-pressed={on}
                  className={vstack({
                    gap: "1.5",
                    alignItems: "stretch",
                    p: "1.5",
                    borderRadius: "md",
                    borderWidth: "1px",
                    borderColor: on ? "accent" : "transparent",
                    bg: on ? "accentQuiet" : "transparent",
                    cursor: "pointer",
                    textAlign: "left",
                    _hover: { bg: "surface" },
                  })}
                >
                  <img
                    src={`/api/comics/${comic.id}/cover`}
                    alt=""
                    loading="lazy"
                    className={css({
                      w: "full",
                      aspectRatio: "0.65",
                      objectFit: "cover",
                      borderRadius: "sm",
                      bg: "ink.800",
                    })}
                  />
                  <span
                    className={css({
                      fontSize: "2xs",
                      fontWeight: "semibold",
                      color: on ? "magenta.300" : "textMuted",
                      lineClamp: "2",
                    })}
                  >
                    {comicLabel(comic)}
                  </span>
                </button>
              );
            })}
          </div>
        )}
      </div>

      <div className={hstack({ gap: "3", justify: "space-between" })}>
        <span className={css({ fontSize: "sm", color: "textMuted" })}>
          {picked.size === 0
            ? "Pick the ones you want"
            : picked.size === 1
              ? "1 picked"
              : `${picked.size} picked`}
        </span>
        <div className={hstack({ gap: "3" })}>
          <Dialog.CloseTrigger asChild>
            <Button variant="ghost">Never mind</Button>
          </Dialog.CloseTrigger>
          <Button
            variant="primary"
            busy={add.isPending}
            disabled={picked.size === 0}
            onClick={() => add.mutate()}
          >
            Add to {cfg.singular}
          </Button>
        </div>
      </div>
    </ModalShell>
  );
}

/** The dialog chrome both of this page's modals sit in. */
function ModalShell({
  open,
  onOpenChange,
  title,
  maxW,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  maxW: "md" | "2xl";
  children: React.ReactNode;
}) {
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
            className={`${maxW === "md" ? SHELL_MD : SHELL_2XL} ${vstack({
              gap: "5",
              alignItems: "stretch",
              w: "full",
              p: "6",
              bg: "surfaceRaised",
              borderWidth: "1px",
              borderColor: "border",
              borderRadius: "xl",
              boxShadow: "pop",
            })}`}
          >
            <Dialog.Title className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}>
              {title}
            </Dialog.Title>
            {children}
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}

// Finished class names per width. Panda extracts at build time and would emit
// nothing at all for a maxW handed in through a variable.
const SHELL_MD = css({ maxW: "md" });
const SHELL_2XL = css({ maxW: "2xl" });

const FIELD = css({
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
});

function BackLink({ kind }: { kind: CollectionKind }) {
  const cfg = KIND_CONFIG[kind];
  return (
    <Link
      to={cfg.basePath}
      className={hstack({
        gap: "2",
        alignSelf: "flex-start",
        fontSize: "sm",
        fontWeight: "semibold",
        color: "textMuted",
        textDecoration: "none",
        _hover: { color: "text" },
      })}
    >
      <ArrowLeft size={15} />
      All {cfg.plural.toLowerCase()}
    </Link>
  );
}
