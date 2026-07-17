import { Link } from "@tanstack/react-router";
import { FolderOpen, Plus, Users } from "lucide-react";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { useAuth } from "../auth/AuthProvider";
import type { Collection } from "../api/generated";

// TODO(Collections): wire to GET /api/collections, POST /api/collections
// (CreateCollectionRequest) behind the "New collection" button, and
// DELETE /api/collections/{id} through ConfirmDialog with tone="danger".
const collections: Collection[] = [];

export function CollectionsPage() {
  const { user } = useAuth();

  const mine = collections.filter((c) => c.ownerId === user?.id);
  const shared = collections.filter((c) => c.ownerId !== user?.id);

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow="Dividers"
        title="Collections"
        subtitle="Group comics however you like. Yours stay private until you share them."
        actions={
          <Button variant="primary" icon={<Plus size={16} />}>
            New collection
          </Button>
        }
      />

      {collections.length === 0 ? (
        <EmptyState icon={FolderOpen} title="No collections yet">
          A collection is a shelf within your shelf — a run of one series, a
          reading order, whatever you want to keep together. Make one and add
          comics to it from the library.
        </EmptyState>
      ) : (
        <div className={vstack({ gap: "8", alignItems: "stretch" })}>
          <CollectionGroup title="Yours" items={mine} />
          {shared.length > 0 && <CollectionGroup title="Shared with you" items={shared} />}
        </div>
      )}
    </div>
  );
}

function CollectionGroup({ title, items }: { title: string; items: Collection[] }) {
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
          <CollectionCard key={collection.id} collection={collection} />
        ))}
      </div>
    </section>
  );
}

function CollectionCard({ collection }: { collection: Collection }) {
  return (
    <Link
      to="/collections/$id"
      params={{ id: collection.id }}
      className={hstack({
        gap: "4",
        p: "4",
        borderRadius: "lg",
        bg: "surface",
        borderWidth: "1px",
        borderColor: "border",
        textDecoration: "none",
        transition: "border-color 0.15s ease, background 0.15s ease",
        _hover: { borderColor: "ink.600", bg: "surfaceRaised" },
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
        <span className={css({ fontWeight: "semibold", color: "text", truncate: true, maxW: "full" })}>
          {collection.name}
        </span>
        <span className={css({ fontSize: "xs", color: "textMuted" })}>
          {collection.count === 1 ? "1 comic" : `${collection.count} comics`}
          {collection.ownerName && ` · ${collection.ownerName}`}
        </span>
        {collection.shared && (
          <span
            className={hstack({
              gap: "1.5",
              mt: "1",
              px: "2",
              py: "0.5",
              borderRadius: "full",
              bg: "accentQuiet",
              color: "magenta.300",
              fontSize: "2xs",
              fontWeight: "bold",
            })}
          >
            <Users size={11} />
            Shared
          </span>
        )}
      </div>
    </Link>
  );
}
