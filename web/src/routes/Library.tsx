import { useState } from "react";
import { Library as LibraryIcon, Search, Upload } from "lucide-react";
import { Link } from "@tanstack/react-router";
import { css } from "styled-system/css";
import { grid, hstack, vstack } from "styled-system/patterns";
import { ComicCard } from "../components/ComicCard";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import { useLiveData } from "../live/LiveData";
import type { Comic, Progress } from "../api/generated";

// TODO(Library): wire to GET /api/comics (and GET /api/progress for the bars).
// The comics list also arrives unprompted over the WS as a `comics` message
// whenever the watcher notices a change, so whatever query lands here should be
// seeded from useLiveData() rather than polling.
const comics: Comic[] = [];
const progressByComic = new Map<string, Progress>();

type Sort = "added" | "title" | "series";

const SORTS: { value: Sort; label: string }[] = [
  { value: "added", label: "Recently added" },
  { value: "title", label: "Title" },
  { value: "series", label: "Series" },
];

export function LibraryPage() {
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<Sort>("added");
  const { library } = useLiveData();

  const shown = comics.filter((c) =>
    query ? `${c.title} ${c.series ?? ""}`.toLowerCase().includes(query.toLowerCase()) : true,
  );

  return (
    <div className={vstack({ gap: "7", alignItems: "stretch" })}>
      <PageHeader
        eyebrow="Shelf"
        title="Library"
        subtitle={
          library?.scanning
            ? "Still reading your shelves — new comics will appear as they turn up."
            : "Everything on this server that you can read."
        }
      />

      <div className={hstack({ gap: "3", flexWrap: "wrap" })}>
        <label
          className={hstack({
            gap: "2.5",
            flex: "1",
            minW: "56",
            px: "3.5",
            py: "2.5",
            borderRadius: "md",
            bg: "surface",
            borderWidth: "1px",
            borderColor: "border",
            _focusWithin: { borderColor: "accent" },
          })}
        >
          <Search size={16} className={css({ color: "ink.500", flexShrink: 0 })} />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search by title or series"
            aria-label="Search the library"
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

        <div className={hstack({ gap: "1", p: "1", borderRadius: "md", bg: "surface", borderWidth: "1px", borderColor: "border" })}>
          {SORTS.map((s) => (
            <button
              key={s.value}
              onClick={() => setSort(s.value)}
              className={css({
                px: "3",
                py: "1.5",
                borderRadius: "sm",
                fontSize: "xs",
                fontWeight: "semibold",
                cursor: "pointer",
                color: sort === s.value ? "text" : "textMuted",
                bg: sort === s.value ? "surfaceRaised" : "transparent",
                _hover: { color: "text" },
              })}
            >
              {s.label}
            </button>
          ))}
        </div>
      </div>

      {shown.length === 0 ? (
        query ? (
          <EmptyState icon={Search} title={`Nothing matches “${query}”`}>
            Try a shorter search, or check the spelling of the series name.
          </EmptyState>
        ) : (
          <EmptyState
            icon={LibraryIcon}
            title="Your shelf is empty"
            action={
              <Link
                to="/import"
                className={hstack({
                  gap: "2",
                  px: "4",
                  py: "2.5",
                  borderRadius: "md",
                  bg: "accent",
                  color: "white",
                  fontSize: "sm",
                  fontWeight: "bold",
                  textDecoration: "none",
                  _hover: { bg: "accentHover" },
                })}
              >
                <Upload size={16} />
                Import comics
              </Link>
            }
          >
            Drop CBZ files into{" "}
            <code className={css({ fontFamily: "mono", color: "ink.200" })}>
              {library?.root ?? "your library folder"}
            </code>{" "}
            and they'll show up here on their own. Or bring in a folder of images
            and Longbox will package it for you.
          </EmptyState>
        )
      ) : (
        <div
          className={grid({
            columns: { base: 2, sm: 3, md: 4, lg: 5, xl: 6 },
            gap: { base: "4", md: "5" },
          })}
        >
          {shown.map((comic) => (
            <ComicCard key={comic.id} comic={comic} progress={progressByComic.get(comic.id)} />
          ))}
        </div>
      )}
    </div>
  );
}
