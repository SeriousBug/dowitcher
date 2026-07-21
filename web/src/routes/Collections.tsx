import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Dialog, Portal } from "@ark-ui/react";
import { Eye, FolderOpen, Plus, Trash2 } from "lucide-react";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { ShareSwitch } from "../components/ShareSwitch";
import { useAuth } from "../auth/AuthProvider";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { KIND_CONFIG, type CollectionKind } from "../lib/collectionKind";
import type { Collection } from "../api/generated";

// The detail route's typed `to` cannot take a template string, so the two kinds
// map to their literal route ids here.
function detailTo(kind: CollectionKind) {
  return kind === "readinglist" ? "/reading-lists/$id" : "/collections/$id";
}

export function CollectionsPage({ kind = "collection" }: { kind?: CollectionKind }) {
  const cfg = KIND_CONFIG[kind];
  const { user } = useAuth();
  const queryClient = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<Collection | null>(null);

  const collectionsQuery = useQuery({
    queryKey: ["collections", kind],
    queryFn: () => http.get<Collection[]>(`/api/collections?kind=${kind}`),
  });

  const remove = useMutation({
    mutationFn: (collection: Collection) =>
      http.del<{ ok: boolean }>(`/api/collections/${collection.id}`),
    onSuccess: (_data, collection) => {
      queryClient.invalidateQueries({ queryKey: ["collections", kind] });
      toaster.create({ type: "success", title: `Deleted ${collection.name}` });
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: `Couldn't delete that ${cfg.singular}`,
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      });
    },
  });

  const collections = collectionsQuery.data ?? [];
  const mine = collections.filter((c) => c.ownerId === user?.id);
  const shared = collections.filter((c) => c.ownerId !== user?.id);

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow={kind === "readinglist" ? "Sequences" : "Dividers"}
        title={cfg.plural}
        subtitle={
          kind === "readinglist"
            ? "Line comics up in the order you mean to read them. Yours stay private until you share them."
            : "Group comics however you like. Yours stay private until you share them."
        }
        actions={
          <Button variant="primary" icon={<Plus size={16} />} onClick={() => setCreating(true)}>
            New {cfg.singular}
          </Button>
        }
      />

      {collectionsQuery.isLoading ? (
        <CollectionSkeleton />
      ) : collectionsQuery.isError ? (
        <EmptyState
          icon={FolderOpen}
          title={`Couldn't load your ${cfg.plural.toLowerCase()}`}
          action={
            <Button variant="primary" onClick={() => collectionsQuery.refetch()}>
              Try again
            </Button>
          }
        >
          {collectionsQuery.error instanceof HttpError
            ? collectionsQuery.error.message
            : "The server didn't answer. It may still be starting up."}
        </EmptyState>
      ) : collections.length === 0 ? (
        <EmptyState
          icon={FolderOpen}
          title={`No ${cfg.plural.toLowerCase()} yet`}
          action={
            <Button variant="primary" icon={<Plus size={16} />} onClick={() => setCreating(true)}>
              New {cfg.singular}
            </Button>
          }
        >
          {kind === "readinglist"
            ? "A reading list is an ordered run — a series in issue order, a crossover in the order it happened. Make one and add comics from the library."
            : "A collection is a shelf within your shelf — a run of one series, a reading order, whatever you want to keep together. Make one and add comics to it from the library."}
        </EmptyState>
      ) : (
        <div className={vstack({ gap: "8", alignItems: "stretch" })}>
          <CollectionGroup
            title="Yours"
            kind={kind}
            items={mine}
            onDelete={(c) => setConfirmDelete(c)}
            owned
          />
          <CollectionGroup title="Shared with you" kind={kind} items={shared} />
        </div>
      )}

      <CreateCollectionDialog kind={kind} open={creating} onOpenChange={setCreating} />

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDelete(null);
        }}
        title={`Delete this ${cfg.singular}?`}
        description={
          <>
            <strong>{confirmDelete?.name}</strong> goes away for good. The comics
            in it stay on your shelf — only the grouping is lost.
          </>
        }
        confirmLabel="Delete"
        tone="danger"
        onConfirm={() => confirmDelete && remove.mutate(confirmDelete)}
      />
    </div>
  );
}

function CollectionGroup({
  title,
  kind,
  items,
  owned = false,
  onDelete,
}: {
  title: string;
  kind: CollectionKind;
  items: Collection[];
  owned?: boolean;
  onDelete?: (collection: Collection) => void;
}) {
  if (items.length === 0) return null;
  return (
    <section className={vstack({ gap: "4", alignItems: "stretch" })}>
      <h2
        className={css({
          fontSize: "2xs",
          fontWeight: "bold",
          letterSpacing: "0.14em",
          textTransform: "uppercase",
          color: "textMuted",
        })}
      >
        {title}
      </h2>
      <div className={grid({ columns: { base: 1, sm: 2, lg: 3 }, gap: "4" })}>
        {items.map((collection) => (
          <CollectionCard
            key={collection.id}
            collection={collection}
            kind={kind}
            owned={owned}
            onDelete={onDelete}
          />
        ))}
      </div>
    </section>
  );
}

/**
 * The card carries the sharing state in the card itself, not behind a click: a
 * shared collection is a thing other people can read, and finding that out by
 * opening it one at a time is how you end up sharing something you didn't mean
 * to. Shared ones wear the accent down their spine edge; private ones don't.
 *
 * The link is the top half only. The footer's switch and delete button are
 * siblings rather than children of it, because a button inside an anchor is a
 * button that navigates when you press it.
 */
function CollectionCard({
  collection,
  kind,
  owned,
  onDelete,
}: {
  collection: Collection;
  kind: CollectionKind;
  owned: boolean;
  onDelete?: (collection: Collection) => void;
}) {
  return (
    <div
      className={vstack({
        gap: "0",
        alignItems: "stretch",
        borderRadius: "lg",
        bg: "surface",
        borderWidth: "1px",
        borderColor: collection.shared ? "magenta.900" : "border",
        borderLeftWidth: "3px",
        borderLeftColor: collection.shared ? "accent" : "ink.700",
        overflow: "hidden",
        transition: "border-color 0.15s ease",
        _hover: { borderColor: collection.shared ? "magenta.700" : "ink.600" },
      })}
    >
      <Link
        to={detailTo(kind)}
        params={{ id: collection.id }}
        className={hstack({
          gap: "4",
          p: "4",
          textDecoration: "none",
          transition: "background 0.15s ease",
          _hover: { bg: "surfaceRaised" },
        })}
      >
        {/* The cover of the first comic stands in for the collection, the way the
            front issue faces you when you flip through a real box. */}
        <span
          className={css({
            w: "14",
            aspectRatio: "0.65",
            borderRadius: "sm",
            bg: "ink.800",
            borderWidth: "1px",
            borderColor: "ink.750",
            overflow: "hidden",
            flexShrink: 0,
          })}
        >
          {collection.coverComicId && (
            <img
              src={`/api/comics/${collection.coverComicId}/cover`}
              alt=""
              loading="lazy"
              className={css({ w: "full", h: "full", objectFit: "cover" })}
            />
          )}
        </span>

        <div className={vstack({ gap: "1", alignItems: "flex-start", minW: "0", flex: "1" })}>
          <span
            className={css({ fontWeight: "semibold", color: "text", truncate: true, maxW: "full" })}
          >
            {collection.name}
          </span>
          <span className={css({ fontSize: "xs", color: "textMuted" })}>
            {collection.count === 1 ? "1 comic" : `${collection.count} comics`}
          </span>
          {collection.summary && (
            <span className={css({ fontSize: "xs", color: "ink.500", lineClamp: "2" })}>
              {collection.summary}
            </span>
          )}
        </div>
      </Link>

      <div
        className={hstack({
          gap: "3",
          justify: "space-between",
          px: "4",
          py: "2.5",
          borderTopWidth: "1px",
          borderColor: "border",
          bg: "ink.850",
        })}
      >
        {owned ? (
          <>
            <ShareSwitch collection={collection} />
            <button
              onClick={() => onDelete?.(collection)}
              aria-label={`Delete ${collection.name}`}
              title={`Delete ${collection.name}`}
              className={css({
                display: "flex",
                p: "1.5",
                borderRadius: "md",
                color: "textMuted",
                cursor: "pointer",
                _hover: { color: "danger", bg: "surfaceRaised" },
              })}
            >
              <Trash2 size={15} />
            </button>
          </>
        ) : (
          // Sharing grants reading and nothing else, and the server will say so
          // with a 403. Offering a switch here would be offering a lie.
          <span
            className={hstack({ gap: "2", fontSize: "xs", color: "textMuted" })}
            title="Shared collections are read-only"
          >
            <Eye size={13} />
            Read-only · shared by {collection.ownerName ?? "someone else"}
          </span>
        )}
      </div>
    </div>
  );
}

function CreateCollectionDialog({
  kind,
  open,
  onOpenChange,
}: {
  kind: CollectionKind;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const cfg = KIND_CONFIG[kind];
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [summary, setSummary] = useState("");
  const [shared, setShared] = useState(false);

  const create = useMutation({
    mutationFn: () =>
      http.post<Collection>("/api/collections", {
        name: name.trim(),
        summary: summary.trim() || undefined,
        shared,
        kind,
      }),
    onSuccess: (collection) => {
      queryClient.invalidateQueries({ queryKey: ["collections", kind] });
      toaster.create({ type: "success", title: `Made ${collection.name}` });
      onOpenChange(false);
      setName("");
      setSummary("");
      setShared(false);
    },
    onError: (err) => {
      toaster.create({
        type: "error",
        title: `Couldn't make that ${cfg.singular}`,
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
            <Dialog.Title className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}>
              New {cfg.singular}
            </Dialog.Title>

            <label className={vstack({ gap: "1.5", alignItems: "stretch" })}>
              <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
                Name
              </span>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={kind === "readinglist" ? "Saga, in order" : "Saga, or Tuesday night reading"}
                autoFocus
                className={FIELD}
              />
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

            <label
              className={hstack({
                gap: "3",
                p: "3.5",
                borderRadius: "md",
                bg: "bg",
                borderWidth: "1px",
                borderColor: shared ? "magenta.900" : "border",
                cursor: "pointer",
              })}
            >
              <input
                type="checkbox"
                checked={shared}
                onChange={(e) => setShared(e.target.checked)}
                className={css({ w: "4", h: "4", accentColor: "accent", cursor: "pointer" })}
              />
              <span className={vstack({ gap: "0.5", alignItems: "flex-start" })}>
                <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
                  Share it with everyone here
                </span>
                <span className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.5" })}>
                  Anyone with an account on this Dowitcher will be able to read what
                  you put in it. They can't change it, and you can turn this off
                  whenever you like.
                </span>
              </span>
            </label>

            <div className={hstack({ gap: "3", justify: "flex-end" })}>
              <Dialog.CloseTrigger asChild>
                <Button variant="ghost">Never mind</Button>
              </Dialog.CloseTrigger>
              <Button
                variant="primary"
                busy={create.isPending}
                disabled={!name.trim()}
                onClick={() => create.mutate()}
              >
                Make it
              </Button>
            </div>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}

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

function CollectionSkeleton() {
  return (
    <div className={grid({ columns: { base: 1, sm: 2, lg: 3 }, gap: "4" })}>
      {Array.from({ length: 6 }, (_, i) => (
        <div
          key={i}
          className={css({
            h: "32",
            borderRadius: "lg",
            bg: "surface",
            borderWidth: "1px",
            borderColor: "border",
            animation: "shimmer 2.4s ease-in-out infinite",
          })}
          style={{ animationDelay: `${i * 0.14}s` }}
        />
      ))}
    </div>
  );
}
