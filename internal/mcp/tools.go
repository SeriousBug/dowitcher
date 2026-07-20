package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
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

type ListComicsInput struct {
	Offset int `json:"offset,omitempty" jsonschema:"how many comics to skip; default 0"`
	Limit  int `json:"limit,omitempty" jsonschema:"maximum comics to return; default 50, max 200"`
}

type ListComicsOutput struct {
	Comics []comicView `json:"comics"`
	Total  int         `json:"total" jsonschema:"total comics visible to you before paging"`
}

func (s *Server) listComics(ctx context.Context, _ *mcp.CallToolRequest, in ListComicsInput) (*mcp.CallToolResult, ListComicsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ListComicsOutput{}, errNoUser
	}
	f := store.ComicFilter{Offset: max(in.Offset, 0), Limit: pageLimit(in.Limit)}
	comics, total, err := s.store.ListComicsFiltered(u.ID, f)
	if err != nil {
		return nil, ListComicsOutput{}, dbErr(err)
	}
	return nil, ListComicsOutput{Comics: viewComics(comics), Total: total}, nil
}

// --- search_comics ---

type SearchComicsInput struct {
	Query      string `json:"query,omitempty" jsonschema:"substring matched against a comic's title or series, case-insensitive"`
	Tag        string `json:"tag,omitempty" jsonschema:"limit to comics carrying this tag of yours"`
	Series     string `json:"series,omitempty" jsonschema:"exact series name to limit to"`
	Collection string `json:"collection,omitempty" jsonschema:"id of a collection to limit to"`
	Offset     int    `json:"offset,omitempty" jsonschema:"how many results to skip; default 0"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum results to return; default 50, max 200"`
}

func (s *Server) searchComics(ctx context.Context, _ *mcp.CallToolRequest, in SearchComicsInput) (*mcp.CallToolResult, ListComicsOutput, error) {
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

// --- tag_comic / untag_comic ---

type TagComicInput struct {
	ComicID string   `json:"comicId" jsonschema:"id of the comic to tag"`
	Tags    []string `json:"tags" jsonschema:"one or more tag names to add"`
}

// tagComic adds tags without dropping the ones already there: the store's tag
// write replaces the caller's whole set on a comic, so an add has to read the
// current set and union the new names in. This is what makes "tag everything as
// read" not silently strip whatever else was on each comic.
func (s *Server) tagComic(ctx context.Context, _ *mcp.CallToolRequest, in TagComicInput) (*mcp.CallToolResult, ComicOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ComicOutput{}, errNoUser
	}
	c, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, notFoundOr(err, "comic")
	}
	merged := mergeTags(c.Tags, in.Tags, nil)
	if err := s.store.SetComicTags(u.ID, in.ComicID, merged); err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	updated, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	return nil, ComicOutput{Comic: viewComic(updated)}, nil
}

func (s *Server) untagComic(ctx context.Context, _ *mcp.CallToolRequest, in TagComicInput) (*mcp.CallToolResult, ComicOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ComicOutput{}, errNoUser
	}
	c, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, notFoundOr(err, "comic")
	}
	remaining := mergeTags(c.Tags, nil, in.Tags)
	if err := s.store.SetComicTags(u.ID, in.ComicID, remaining); err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	updated, err := s.store.GetComic(u.ID, in.ComicID)
	if err != nil {
		return nil, ComicOutput{}, dbErr(err)
	}
	return nil, ComicOutput{Comic: viewComic(updated)}, nil
}

// --- list_collections / create_collection / add_to_collection ---

type ListCollectionsOutput struct {
	Collections []api.Collection `json:"collections"`
}

func (s *Server) listCollections(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListCollectionsOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, ListCollectionsOutput{}, errNoUser
	}
	cols, err := s.store.ListCollections(u.ID)
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
	col, err := s.store.CreateCollection(store.NewID(), u.ID, name, strings.TrimSpace(in.Summary), in.Shared)
	if err != nil {
		return nil, CollectionOutput{}, dbErr(err)
	}
	return nil, CollectionOutput{Collection: col}, nil
}

type AddToCollectionInput struct {
	CollectionID string `json:"collectionId" jsonschema:"id of one of your own collections"`
	ComicID      string `json:"comicId" jsonschema:"id of the comic to add"`
}

func (s *Server) addToCollection(ctx context.Context, _ *mcp.CallToolRequest, in AddToCollectionInput) (*mcp.CallToolResult, okOutput, error) {
	u, ok := callerFrom(ctx)
	if !ok {
		return nil, okOutput{}, errNoUser
	}
	if err := s.store.AddToCollection(u.ID, in.CollectionID, in.ComicID); err != nil {
		// A miss here is either an unowned/absent collection or a comic the caller
		// can't see; the store returns the same sentinel for both by design.
		return nil, okOutput{}, notFoundOr(err, "collection or comic")
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
