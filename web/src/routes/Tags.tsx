import { useState } from "react";
import { Tags as TagsIcon } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { EmptyState } from "../components/EmptyState";
import { PageHeader } from "../components/PageHeader";
import type { Tag } from "../api/generated";

// TODO(Tags): wire to GET /api/tags. Selecting a tag should hand off to the
// library filtered by it rather than listing comics a second time here.
const tags: Tag[] = [];

export function TagsPage() {
  const [selected, setSelected] = useState<string | null>(null);

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

      {tags.length === 0 ? (
        <EmptyState icon={TagsIcon} title="No tags yet">
          Open any comic's details and add a tag. Once a few comics are labelled,
          this becomes the fastest way around your library.
        </EmptyState>
      ) : (
        <div className={hstack({ gap: "2.5", flexWrap: "wrap" })}>
          {tags.map((tag) => {
            const weight = max > 0 ? tag.count / max : 0;
            const active = selected === tag.name;
            return (
              <button
                key={tag.name}
                onClick={() => setSelected(active ? null : tag.name)}
                className={hstack({
                  gap: "2",
                  px: "3.5",
                  py: "2",
                  borderRadius: "full",
                  borderWidth: "1px",
                  borderColor: active ? "accent" : "border",
                  bg: active ? "accentQuiet" : "surface",
                  color: active ? "text" : "textMuted",
                  fontWeight: "semibold",
                  cursor: "pointer",
                  transition: "all 0.15s ease",
                  _hover: { borderColor: active ? "accent" : "ink.600", color: "text" },
                })}
                style={{ fontSize: `${0.8 + weight * 0.35}rem` }}
              >
                {tag.name}
                <span
                  className={css({
                    fontFamily: "mono",
                    fontSize: "2xs",
                    color: active ? "magenta.300" : "ink.500",
                  })}
                >
                  {tag.count}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
