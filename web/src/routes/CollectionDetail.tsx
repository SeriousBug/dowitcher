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
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { ShareSwitch } from "../components/ShareSwitch";
import { useAuth } from "../auth/AuthProvider";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { fetchComics } from "../api/comics";
import { comicLabel } from "../lib/format";
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

export function CollectionDetailPage({ id }: { id: string }) {
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

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["comics", { collection: id }] });
    queryClient.invalidateQueries({ queryKey: ["collection", id] });
    queryClient.invalidateQueries({ queryKey: ["collections"] });
  };

  const removeCollection = useMutation({
    mutationFn: () => http.del<{ ok: boolean }>(`/api/collections/${id}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["collections"] });
      toaster.create({ type: "success", title: `Deleted ${collection?.name ?? "collection"}` });
      navigate({ to: "/collections" });
    },
    onError: failed("Couldn't delete that collection"),
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

  // The whole order goes up, not a move instruction: the server keeps positions
  // dense from the full list, which makes a retry after a dropped response
  // idempotent instead of a scramble.
  const reorder = useMutation({
    mutationFn: (comicIds: string[]) =>
      http.put<{ ok: boolean }>(`/api/collections/${id}/order`, { comicIds }),
    onSuccess: () => {
      invalidate();
      toaster.create({ type: "success", title: "Reordered" });
    },
    onError: failed("Couldn't save that order"),
  });

  function move(index: number, by: number) {
    const next = [...comics];
    const to = index + by;
    if (to < 0 || to >= next.length) return;
    [next[index], next[to]] = [next[to], next[index]];
    reorder.mutate(next.map((c) => c.id));
  }

  if (collectionQuery.isLoading) {
    return (
      <div className={vstack({ gap: "7", alignItems: "stretch" })}>
        <BackLink />
        <ComicGridSkeleton />
      </div>
    );
  }

  if (!collection) {
    return (
      <div className={vstack({ gap: "6", alignItems: "stretch" })}>
        <BackLink />
        <EmptyState icon={Users} title="This collection isn't here">
          It may have been deleted, or its owner stopped sharing it. Reference:{" "}
          <code className={css({ fontFamily: "mono" })}>{id}</code>
        </EmptyState>
      </div>
    );
  }

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <BackLink />

      <PageHeader
        eyebrow="Collection"
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
                aria-label="Edit collection"
                title="Edit collection"
                onClick={() => setEditing(true)}
              />
              <Button
                variant="ghost"
                icon={<Trash2 size={16} />}
                aria-label="Delete collection"
                title="Delete collection"
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
      ) : comics.length === 0 ? (
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
          {comics.map((comic, i) => (
            <ComicTile
              key={comic.id}
              comic={comic}
              progress={comicsQuery.data?.progress.get(comic.id)}
              actions={
                owned ? (
                  <>
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
                      label={`Take ${comicLabel(comic)} out of this collection`}
                      onClick={() => removeComic.mutate(comic)}
                    >
                      <X size={14} />
                    </TileButton>
                  </>
                ) : undefined
              }
            />
          ))}
        </ComicGrid>
      )}

      <EditCollectionDialog
        collection={collection}
        open={editing}
        onOpenChange={setEditing}
        onSaved={invalidate}
      />

      <AddComicsDialog
        collectionId={id}
        alreadyIn={new Set(comics.map((c) => c.id))}
        open={adding}
        onOpenChange={setAdding}
        onAdded={invalidate}
      />

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete this collection?"
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
  );
}

function EditCollectionDialog({
  collection,
  open,
  onOpenChange,
  onSaved,
}: {
  collection: Collection;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved: () => void;
}) {
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
    <ModalShell open={open} onOpenChange={onOpenChange} title="Edit collection" maxW="md">
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
  alreadyIn,
  open,
  onOpenChange,
  onAdded,
}: {
  collectionId: string;
  alreadyIn: Set<string>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onAdded: () => void;
}) {
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
            {q ? `Nothing left to add matching “${q}”.` : "Everything you can read is already in here."}
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
            Add to collection
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

function BackLink() {
  return (
    <Link
      to="/collections"
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
      All collections
    </Link>
  );
}
