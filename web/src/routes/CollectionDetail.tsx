import { Link } from "@tanstack/react-router";
import { ArrowLeft, BookPlus, Pencil, Trash2, Users } from "lucide-react";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ComicCard } from "../components/ComicCard";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import type { Collection, Comic } from "../api/generated";

// TODO(CollectionDetail): wire to GET /api/collections/{id} and
// GET /api/collections/{id}/comics. The edit button takes an
// UpdateCollectionRequest via PUT; the shared toggle is the same call with only
// `shared` set. Removal is DELETE /api/collections/{id}/comics/{comicId}.
const collection: Collection | null = null;
const comics: Comic[] = [];

export function CollectionDetailPage({ id }: { id: string }) {
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
        subtitle={collection.summary || `${collection.count} comics · ${collection.ownerName ?? ""}`}
        actions={
          <>
            <Button icon={<BookPlus size={16} />}>Add comics</Button>
            <Button icon={<Pencil size={16} />} aria-label="Edit collection" title="Edit collection" />
            <Button
              variant="ghost"
              icon={<Trash2 size={16} />}
              aria-label="Delete collection"
              title="Delete collection"
            />
          </>
        }
      />

      {comics.length === 0 ? (
        <EmptyState icon={BookPlus} title="Nothing in here yet">
          Add comics from your library and they'll line up here in the order you
          put them.
        </EmptyState>
      ) : (
        <div className={grid({ columns: { base: 2, sm: 3, md: 4, lg: 5, xl: 6 }, gap: { base: "4", md: "5" } })}>
          {comics.map((comic) => (
            <ComicCard key={comic.id} comic={comic} />
          ))}
        </div>
      )}
    </div>
  );
}

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
