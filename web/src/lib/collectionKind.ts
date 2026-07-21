/**
 * Collections and reading lists are the same server-side resource split by a
 * `kind` field; the difference is entirely presentational. This is the one place
 * that maps a kind to its words and its route, so the Collections and Reading
 * lists pages are the same components handed a different config.
 */
export type CollectionKind = "collection" | "readinglist";

export interface KindConfig {
  kind: CollectionKind;
  /** URL prefix for this kind's list and detail routes. */
  basePath: "/collections" | "/reading-lists";
  /** "Collections" / "Reading lists" — page title, nav label. */
  plural: string;
  /** "Collection" / "Reading list" — an eyebrow or a capitalised noun. */
  singularCap: string;
  /** "collection" / "reading list" — mid-sentence noun. */
  singular: string;
}

export const KIND_CONFIG: Record<CollectionKind, KindConfig> = {
  collection: {
    kind: "collection",
    basePath: "/collections",
    plural: "Collections",
    singularCap: "Collection",
    singular: "collection",
  },
  readinglist: {
    kind: "readinglist",
    basePath: "/reading-lists",
    plural: "Reading lists",
    singularCap: "Reading list",
    singular: "reading list",
  },
};
