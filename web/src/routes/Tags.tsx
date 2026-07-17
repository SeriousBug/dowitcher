import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Tags as TagsIcon } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { http, HttpError } from "../api/http";
import type { Tag } from "../api/generated";

export function TagsPage() {
  const tagsQuery = useQuery({
    queryKey: ["tags"],
    queryFn: () => http.get<Tag[]>("/api/tags"),
  });

  const tags = tagsQuery.data ?? [];

  // Weight each tag by how much of the library it covers, so the shape of the
  // collection is readable at a glance instead of alphabetically flat.
  const max = tags.reduce((m, t) => Math.max(m, t.count), 0);

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow="Index"
        title="Tags"
        subtitle="Everyone on this server shares one set of tags, so a label means the same thing to all of you."
      />

      {tagsQuery.isLoading ? (
        <TagSkeleton />
      ) : tagsQuery.isError ? (
        <EmptyState
          icon={TagsIcon}
          title="Couldn't load your tags"
          action={
            <Button variant="primary" onClick={() => tagsQuery.refetch()}>
              Try again
            </Button>
          }
        >
          {tagsQuery.error instanceof HttpError
            ? tagsQuery.error.message
            : "The server didn't answer. It may still be starting up."}
        </EmptyState>
      ) : tags.length === 0 ? (
        <EmptyState icon={TagsIcon} title="No tags yet">
          Hover a cover in your library and hit the tag button to label it. Once a
          few comics are labelled, this becomes the fastest way around your
          library.
        </EmptyState>
      ) : (
        <div className={hstack({ gap: "2.5", flexWrap: "wrap" })}>
          {tags.map((tag) => {
            const weight = max > 0 ? tag.count / max : 0;
            return (
              // A tag is a way into the library, not a place of its own: picking
              // one hands off to the shelf filtered by it rather than listing the
              // same covers a second time under a different heading.
              <Link
                key={tag.name}
                to="/"
                search={{ tag: tag.name }}
                className={hstack({
                  gap: "2",
                  px: "3.5",
                  py: "2",
                  borderRadius: "full",
                  borderWidth: "1px",
                  borderColor: "border",
                  bg: "surface",
                  color: "textMuted",
                  fontWeight: "semibold",
                  textDecoration: "none",
                  cursor: "pointer",
                  transition: "all 0.15s ease",
                  _hover: { borderColor: "accent", color: "text", bg: "accentQuiet" },
                })}
                style={{ fontSize: `${0.8 + weight * 0.35}rem` }}
              >
                {tag.name}
                <span className={css({ fontFamily: "mono", fontSize: "2xs", color: "ink.500" })}>
                  {tag.count}
                </span>
              </Link>
            );
          })}
        </div>
      )}
    </div>
  );
}

function TagSkeleton() {
  // Varied widths, because a row of identical pills reads as a table, and the
  // real thing never looks like that.
  const widths = ["5rem", "7rem", "4rem", "9rem", "6rem", "5.5rem", "8rem", "4.5rem"];
  return (
    <div className={hstack({ gap: "2.5", flexWrap: "wrap" })}>
      {widths.map((w, i) => (
        <span
          key={i}
          className={css({
            h: "9",
            borderRadius: "full",
            bg: "surface",
            animation: "shimmer 2.4s ease-in-out infinite",
          })}
          style={{ width: w, animationDelay: `${i * 0.12}s` }}
        />
      ))}
    </div>
  );
}
