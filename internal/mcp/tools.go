package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// comicView is the compact shape a tool returns for a comic: enough to identify
// and organise it, without the reader-only fields (page dimensions, cover paths)
// an agent has no use for. Tags are the caller's own, as everywhere else.
type comicView struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Series    string   `json:"series,omitempty"`
	Number    string   `json:"number,omitempty"`
	PageCount int      `json:"pageCount"`
	Tags      []string `json:"tags"`
	// Source is "library", "upload" or "claimed"; a comic can only be claimed
	// when it is "library".
	Source string `json:"source"`
	// OwnedByMe is true for your own uploads and comics you have claimed.
	OwnedByMe bool `json:"ownedByMe"`
}

func viewComic(c api.Comic) comicView {
	tags := c.Tags
	if tags == nil {
		tags = []string{}
	}
	return comicView{
		ID: c.ID, Title: c.Title, Series: c.Series, Number: c.Number,
		PageCount: c.PageCount, Tags: tags, Source: c.Source, OwnedByMe: c.OwnedByMe,
	}
}

func viewComics(cs []api.Comic) []comicView {
	out := make([]comicView, 0, len(cs))
	for _, c := range cs {
		out = append(out, viewComic(c))
	}
	return out
}

// --- list_comics ---

// ListComicsInput is both the browse and the search shape: every field is a
// filter, and no filters means the whole library. Splitting these into two tools
// only made an agent guess which one it wanted.
type ListComicsInput struct {
	Query      string `json:"query,omitempty" jsonschema:"substring matched against a comic's title or series, case-insensitive"`
	Tag        string `json:"tag,omitempty" jsonschema:"limit to comics carrying this tag of yours"`
	Series     string `json:"series,omitempty" jsonschema:"exact series name to limit to"`
	Collection string `json:"collection,omitempty" jsonschema:"id of a collection to limit to"`
	Offset     int    `json:"offset,omitempty" jsonschema:"how many results to skip; default 0"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum results to return; default 50, max 200"`
}

type ListComicsOutput struct {
	Comics []comicView `json:"comics"`
	Total  int         `json:"total" jsonschema:"total comics matching before paging"`
}

func (s *Server) listComics(ctx context.Context, _ *mcp.CallToolRequest, in ListComicsInput) (*mcp.CallToolResult, ListComicsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ListComicsOutput{}, errNoUser
	}
	f := store.ComicFilter{
		Query:      in.Query,
		Tag:        in.Tag,
		Series:     in.Series,
		Collection: in.Collection,
		Offset:     max(in.Offset, 0),
		Limit:      pageLimit(in.Limit),
	}
	comics, total, err := s.store.ListComicsFiltered(u.ID, f)
	if err != nil {
		return nil, ListComicsOutput{}, dbErr(err)
	}
	return nil, ListComicsOutput{Comics: viewComics(comics), Total: total}, nil
}

// --- get_comic ---

type ComicIDInput struct {
	ComicID string `json:"comicId" jsonschema:"id of the comic"`
}

type ComicOutput struct {
	Comic comicView `json:"comic"`
}

func (s *Server) getComic(ctx context.Context, _ *mcp.CallToolRequest, in ComicIDInput) (*mcp.CallToolResult, ComicOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ComicOutput{}, errNoUser
	}
	c, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, notFoundOr(err, "comic")
	}
	return nil, ComicOutput{Comic: viewComic(c)}, nil
}

// --- list_tags ---

type ListTagsOutput struct {
	Tags []api.Tag `json:"tags"`
}

func (s *Server) listTags(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListTagsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ListTagsOutput{}, errNoUser
	}
	tags, err := s.store.ListTags(u.ID)
	if err != nil {
		return nil, ListTagsOutput{}, dbErr(err)
	}
	if tags == nil {
		tags = []api.Tag{}
	}
	return nil, ListTagsOutput{Tags: tags}, nil
}

// --- tag_comics ---

// TagComicsInput tags and untags a set of comics in one call. comicIds is a list
// so "tag these forty issues as read" is one tool call rather than forty, and
// add and remove travel together so a retag is one atomic write per comic
// instead of two calls racing each other.
type TagComicsInput struct {
	ComicIDs []string `json:"comicIds" jsonschema:"ids of the comics to retag"`
	Add      []string `json:"add,omitempty" jsonschema:"tag names to add to every comic listed"`
	Remove   []string `json:"remove,omitempty" jsonschema:"tag names to remove from every comic listed"`
}

// BulkTagOutput reports what a bulk retag actually touched: the comics that were
// updated, plus the ids that could not be seen so the caller learns which of a
// long list was skipped rather than getting a bare error for the batch.
type BulkTagOutput struct {
	Comics  []comicView `json:"comics"`
	Skipped []string    `json:"skipped,omitempty" jsonschema:"ids that were not found or not visible to you"`
}

// tagComics rewrites each comic's tag set through mergeTags rather than writing
// the requested names outright: the store's tag write replaces the caller's whole
// set on a comic, so anything already there has to be read and carried across.
// A comic the caller cannot see is recorded in Skipped rather than failing the
// batch, so a stray id in a long list does not undo the comics that did change.
func (s *Server) tagComics(ctx context.Context, _ *mcp.CallToolRequest, in TagComicsInput) (*mcp.CallToolResult, BulkTagOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, BulkTagOutput{}, errNoUser
	}
	out := BulkTagOutput{Comics: []comicView{}}
	for _, id := range in.ComicIDs {
		c, err := s.store.GetComic(u.ID, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out.Skipped = append(out.Skipped, id)
				continue
			}
			return nil, BulkTagOutput{}, dbErr(err)
		}
		next := mergeTags(c.Tags, in.Add, in.Remove)
		if err := s.store.SetComicTags(u.ID, id, next); err != nil {
			return nil, BulkTagOutput{}, dbErr(err)
		}
		updated, err := s.store.GetComic(u.ID, id)
		if err != nil {
			return nil, BulkTagOutput{}, dbErr(err)
		}
		out.Comics = append(out.Comics, viewComic(updated))
	}
	return nil, out, nil
}

// --- rename_comic ---

type RenameComicInput struct {
	ComicID string `json:"comicId" jsonschema:"id of the comic to rename"`
	Title   string `json:"title" jsonschema:"the new display title"`
}

// renameComic sets a comic's display title. The same rule as the HTTP layer: the
// owner of an upload or claim may rename it, and an admin may rename anything.
// The HTTP handler reads this off the row; here it is checked explicitly, the
// same shape as claim's admin gate.
func (s *Server) renameComic(ctx context.Context, _ *mcp.CallToolRequest, in RenameComicInput) (*mcp.CallToolResult, ComicOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ComicOutput{}, errNoUser
	}
	if _, err := s.store.GetComic(u.ID, in.ComicID); err != nil {
		return nil, ComicOutput{}, notFoundOr(err, "comic")
	}
	row, err := s.store.ComicRowByID(in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	if row.OwnerID != u.ID && !u.IsAdmin {
		return nil, ComicOutput{}, errors.New("only the owner or an admin can rename this comic")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, ComicOutput{}, errors.New("title is required")
	}
	if err := s.store.RenameComic(in.ComicID, title); err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	updated, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	return nil, ComicOutput{Comic: viewComic(updated)}, nil
}

// --- list_collections / create_collection ---

type ListCollectionsInput struct {
	Kind string `json:"kind,omitempty" jsonschema:"limit to one kind: 'collection' or 'readinglist'; omit for both"`
}

type ListCollectionsOutput struct {
	Collections []api.Collection `json:"collections"`
}

func (s *Server) listCollections(ctx context.Context, _ *mcp.CallToolRequest, in ListCollectionsInput) (*mcp.CallToolResult, ListCollectionsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ListCollectionsOutput{}, errNoUser
	}
	cols, err := s.store.ListCollections(u.ID, in.Kind)
	if err != nil {
		return nil, ListCollectionsOutput{}, dbErr(err)
	}
	if cols == nil {
		cols = []api.Collection{}
	}
	return nil, ListCollectionsOutput{Collections: cols}, nil
}

type CreateCollectionInput struct {
	Name    string `json:"name" jsonschema:"name of the collection"`
	Summary string `json:"summary,omitempty" jsonschema:"optional description"`
	Shared  bool   `json:"shared,omitempty" jsonschema:"if true, every user on the server can read it; default false"`
	Kind    string `json:"kind,omitempty" jsonschema:"'collection' (default) or 'readinglist' for an ordered reading list"`
}

type CollectionOutput struct {
	Collection api.Collection `json:"collection"`
}

func (s *Server) createCollection(ctx context.Context, _ *mcp.CallToolRequest, in CreateCollectionInput) (*mcp.CallToolResult, CollectionOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, CollectionOutput{}, errNoUser
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, CollectionOutput{}, errors.New("name is required")
	}
	col, err := s.store.CreateCollection(store.NewID(), u.ID, name, strings.TrimSpace(in.Summary), in.Kind, in.Shared)
	if err != nil {
		return nil, CollectionOutput{}, dbErr(err)
	}
	return nil, CollectionOutput{Collection: col}, nil
}

// --- update_collection / delete_collection ---

type UpdateCollectionInput struct {
	CollectionID string  `json:"collectionId" jsonschema:"id of one of your own collections"`
	Name         *string `json:"name,omitempty" jsonschema:"new name; omit to leave unchanged"`
	Summary      *string `json:"summary,omitempty" jsonschema:"new description; omit to leave unchanged"`
	Shared       *bool   `json:"shared,omitempty" jsonschema:"share with everyone (true) or make private (false); omit to leave unchanged"`
	CoverComicID *string `json:"coverComicId,omitempty" jsonschema:"id of a comic whose cover represents the collection; omit to leave unchanged"`
}

func (s *Server) updateCollection(ctx context.Context, _ *mcp.CallToolRequest, in UpdateCollectionInput) (*mcp.CallToolResult, CollectionOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, CollectionOutput{}, errNoUser
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return nil, CollectionOutput{}, errors.New("name cannot be blank")
	}
	// The cover and the other fields are two separate store writes, so an
	// unreachable cover comic has to be caught before either lands. Otherwise a
	// rename would stick and only the cover would fail, leaving the caller with a
	// half-applied edit it never asked for.
	if in.CoverComicID != nil {
		if _, err := s.store.OwnedCollection(u.ID, in.CollectionID); err != nil {
			return nil, CollectionOutput{}, notFoundOr(err, "collection")
		}
		if _, err := s.store.GetComic(u.ID, *in.CoverComicID); err != nil {
			return nil, CollectionOutput{}, notFoundOr(err, "cover comic")
		}
	}
	req := api.UpdateCollectionRequest{Name: in.Name, Summary: in.Summary, Shared: in.Shared}
	if err := s.store.UpdateCollection(u.ID, in.CollectionID, req); err != nil {
		return nil, CollectionOutput{}, notFoundOr(err, "collection")
	}
	if in.CoverComicID != nil {
		if err := s.store.SetCollectionCover(u.ID, in.CollectionID, *in.CoverComicID); err != nil {
			return nil, CollectionOutput{}, notFoundOr(err, "collection or comic")
		}
	}
	col, err := s.store.GetCollection(u.ID, in.CollectionID)
	if err != nil {
		return nil, CollectionOutput{}, dbErr(err)
	}
	return nil, CollectionOutput{Collection: col}, nil
}

type CollectionIDInput struct {
	CollectionID string `json:"collectionId" jsonschema:"id of one of your own collections"`
}

func (s *Server) deleteCollection(ctx context.Context, _ *mcp.CallToolRequest, in CollectionIDInput) (*mcp.CallToolResult, okOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, okOutput{}, errNoUser
	}
	if err := s.store.DeleteCollection(u.ID, in.CollectionID); err != nil {
		return nil, okOutput{}, notFoundOr(err, "collection")
	}
	return nil, okOutput{OK: true}, nil
}

// --- edit_collection_comics / reorder_collection ---

// EditCollectionComicsInput changes a collection's membership in one call. Adds
// and removes travel together because a "swap these issues for those" edit is one
// intent, and adds land in the order given: the store appends at MAX(position)+1,
// so the caller's order is the resulting reading order.
type EditCollectionComicsInput struct {
	CollectionID string   `json:"collectionId" jsonschema:"id of one of your own collections"`
	Add          []string `json:"add,omitempty" jsonschema:"ids of comics to append, in the order you want them"`
	Remove       []string `json:"remove,omitempty" jsonschema:"ids of comics to drop from the collection"`
}

// EditCollectionComicsOutput returns the collection as it now stands, plus the
// ids that did not apply, matching BulkTagOutput: one unseen or absent comic in a
// long list should not undo the ones that did move.
type EditCollectionComicsOutput struct {
	Collection api.Collection `json:"collection"`
	Skipped    []string       `json:"skipped,omitempty" jsonschema:"ids that were not visible to you, or not in the collection to begin with"`
}

func (s *Server) editCollectionComics(ctx context.Context, _ *mcp.CallToolRequest, in EditCollectionComicsInput) (*mcp.CallToolResult, EditCollectionComicsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, EditCollectionComicsOutput{}, errNoUser
	}
	// A collection the caller does not own fails the whole call: unlike a bad
	// comic id, nothing in the batch could have applied.
	if _, err := s.store.OwnedCollection(u.ID, in.CollectionID); err != nil {
		return nil, EditCollectionComicsOutput{}, notFoundOr(err, "collection")
	}
	var out EditCollectionComicsOutput
	// Removals run first so a call that swaps one comic for another cannot have
	// the removal undo the add.
	for _, id := range in.Remove {
		if err := s.store.RemoveFromCollection(u.ID, in.CollectionID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out.Skipped = append(out.Skipped, id)
				continue
			}
			return nil, EditCollectionComicsOutput{}, dbErr(err)
		}
	}
	for _, id := range in.Add {
		if err := s.store.AddToCollection(u.ID, in.CollectionID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				out.Skipped = append(out.Skipped, id)
				continue
			}
			return nil, EditCollectionComicsOutput{}, dbErr(err)
		}
	}
	col, err := s.store.GetCollection(u.ID, in.CollectionID)
	if err != nil {
		return nil, EditCollectionComicsOutput{}, dbErr(err)
	}
	out.Collection = col
	return nil, out, nil
}

type ReorderCollectionInput struct {
	CollectionID string   `json:"collectionId" jsonschema:"id of one of your own collections"`
	ComicIDs     []string `json:"comicIds" jsonschema:"the collection's comic ids in the order you want them"`
}

func (s *Server) reorderCollection(ctx context.Context, _ *mcp.CallToolRequest, in ReorderCollectionInput) (*mcp.CallToolResult, okOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, okOutput{}, errNoUser
	}
	if err := s.store.ReorderCollection(u.ID, in.CollectionID, in.ComicIDs); err != nil {
		return nil, okOutput{}, notFoundOr(err, "collection")
	}
	return nil, okOutput{OK: true}, nil
}

// --- claim_comic (admin) ---

func (s *Server) claimComic(ctx context.Context, _ *mcp.CallToolRequest, in ComicIDInput) (*mcp.CallToolResult, ComicOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ComicOutput{}, errNoUser
	}
	// The HTTP route gets this gate from requireAdmin; here it has to be explicit.
	if !u.IsAdmin {
		return nil, ComicOutput{}, errors.New("claiming a comic is an admin action")
	}
	if err := s.store.ClaimComic(u.ID, in.ComicID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ComicOutput{}, errors.New("only comics from the watched library folder can be claimed")
		}
		return nil, ComicOutput{}, dbErr(err)
	}
	c, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	return nil, ComicOutput{Comic: viewComic(c)}, nil
}

// --- set_comic_hidden / list_hidden_comics (admin) ---

// SetComicHiddenInput carries hidden as a required field with no default, so
// hiding a comic is never something an agent can do by leaving a field out.
type SetComicHiddenInput struct {
	ComicID string `json:"comicId" jsonschema:"id of the comic"`
	Hidden  bool   `json:"hidden" jsonschema:"true to take the comic off every shelf, false to put it back"`
}

func (s *Server) setComicHidden(ctx context.Context, _ *mcp.CallToolRequest, in SetComicHiddenInput) (*mcp.CallToolResult, okOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, okOutput{}, errNoUser
	}
	// The HTTP route gets this gate from requireAdmin; here it has to be explicit.
	if !u.IsAdmin {
		return nil, okOutput{}, errors.New("hiding a comic is an admin action")
	}
	// Unhiding needs the hidden-inclusive lookup, since the ordinary one can no
	// longer see the row it is about to restore.
	var err error
	if in.Hidden {
		_, err = s.store.GetComic(u.ID, in.ComicID)
	} else {
		_, err = s.store.GetComicWithHidden(u.ID, in.ComicID)
	}
	if err != nil {
		return nil, okOutput{}, notFoundOr(err, "comic")
	}
	if err := s.store.SetComicHidden(in.ComicID, in.Hidden); err != nil {
		return nil, okOutput{}, notFoundOr(err, "comic")
	}
	return nil, okOutput{OK: true}, nil
}

func (s *Server) listHiddenComics(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListComicsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ListComicsOutput{}, errNoUser
	}
	if !u.IsAdmin {
		return nil, ListComicsOutput{}, errors.New("listing hidden comics is an admin action")
	}
	comics, err := s.store.ListHiddenComics(u.ID)
	if err != nil {
		return nil, ListComicsOutput{}, dbErr(err)
	}
	return nil, ListComicsOutput{Comics: viewComics(comics), Total: len(comics)}, nil
}

// --- create_download_link ---

// downloadTTL is how long a minted link stays good. It is short because the URL
// is the whole credential: anyone holding it can fetch that one CBZ, so it has
// to stop working long before it can be filed away and forgotten.
const downloadTTL = time.Hour

type DownloadLinkOutput struct {
	URL string `json:"url" jsonschema:"absolute URL that downloads the comic's CBZ file"`
	// ExpiresAt is Unix seconds, matching every other timestamp the API returns.
	ExpiresAt int64 `json:"expiresAt" jsonschema:"Unix seconds after which the URL stops working"`
}

func (s *Server) createDownloadLink(ctx context.Context, _ *mcp.CallToolRequest, in ComicIDInput) (*mcp.CallToolResult, DownloadLinkOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, DownloadLinkOutput{}, errNoUser
	}
	c, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, DownloadLinkOutput{}, notFoundOr(err, "comic")
	}
	secret := auth.RandToken(32)
	exp := time.Now().Add(downloadTTL)
	if err := s.store.CreateDownloadToken(auth.HashToken(secret), c.ID, u.ID, exp.Unix()); err != nil {
		return nil, DownloadLinkOutput{}, dbErr(err)
	}
	return nil, DownloadLinkOutput{
		URL:       fmt.Sprintf("%s/comics/%s/download/%s", strings.TrimSuffix(s.origin, "/"), c.ID, secret),
		ExpiresAt: exp.Unix(),
	}, nil
}

// --- delete_comic ---

func (s *Server) deleteComic(ctx context.Context, _ *mcp.CallToolRequest, in ComicIDInput) (*mcp.CallToolResult, okOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, okOutput{}, errNoUser
	}
	// Visibility first, so a comic the caller cannot see reads as missing rather
	// than as one they lack permission to delete.
	if _, err := s.store.GetComic(u.ID, in.ComicID); err != nil {
		return nil, okOutput{}, notFoundOr(err, "comic")
	}
	row, err := s.store.ComicRowByID(in.ComicID)
	if err != nil {
		return nil, okOutput{}, dbErr(err)
	}
	if err := store.CanDeleteComic(row, u.ID, u.IsAdmin); err != nil {
		return nil, okOutput{}, err
	}
	if err := s.store.DeleteComic(in.ComicID); err != nil {
		return nil, okOutput{}, notFoundOr(err, "comic")
	}
	// Same order as the HTTP handler: a leftover file is dead weight, whereas a
	// row whose file is gone is a comic that opens to an error. CanDeleteComic
	// has already ruled out every source that lives under the library root.
	if err := os.Remove(filepath.Join(s.uploadsDir, row.Path)); err != nil && !os.IsNotExist(err) {
		log.Printf("mcp delete comic file %s: %v", row.Path, err)
	}
	return nil, okOutput{OK: true}, nil
}

type okOutput struct {
	OK bool `json:"ok"`
}

var errNoUser = errors.New("no authenticated user on request")

// pageLimit clamps a caller-supplied page size. 0 means the default rather than
// "everything", so a forgetful agent does not pull the whole library into a
// single tool result.
func pageLimit(n int) int {
	const def, maxLimit = 50, 200
	if n <= 0 {
		return def
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// mergeTags returns the trimmed union of current and add, minus remove,
// preserving current order then appended additions, dropping duplicates and
// blanks. Comparison is case-sensitive because a tag is a literal string the
// user coined, not a normalised token.
func mergeTags(current, add, remove []string) []string {
	drop := map[string]bool{}
	for _, r := range remove {
		drop[strings.TrimSpace(r)] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, name := range append(append([]string{}, current...), add...) {
		name = strings.TrimSpace(name)
		if name == "" || drop[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func dbErr(err error) error { return fmt.Errorf("store error: %w", err) }

// notFoundOr maps the store's not-found sentinel to a message naming what was
// missing, and passes anything else through as a generic store error.
func notFoundOr(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s not found or not visible to you", what)
	}
	return dbErr(err)
}
