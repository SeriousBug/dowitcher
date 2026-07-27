// Package mcp exposes the library as a Model Context Protocol server so a user
// can point a headless AI agent at their instance and manage comics
// conversationally. It is opt-in: the operator enables it explicitly, and every
// request authenticates with an OAuth 2.1 access token that binds it to exactly
// one user. This package is the resource-server half; the authorization server
// (/authorize, /token, /register) lives in internal/server.
//
// The tools map directly onto the store layer rather than the HTTP handlers, so
// visibility and sharing stay enforced in SQL where they already are. The one
// rule this package adds on top is the admin gate on claim, which the HTTP layer
// gets from requireAdmin and this layer has to apply itself.
//
// On the filesystem the rule is the same one the whole binary keeps: reads may
// reach anywhere a comic's file lives, the library root included, but writes and
// deletes may only ever touch the uploads dir. read_comic_pages therefore opens
// library comics; delete_comic removes files only from uploadsDir, and
// store.CanDeleteComic is what keeps a library-managed comic out of its reach.
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/ocr"
	"github.com/SeriousBug/dowitcher/internal/store"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server builds the MCP HTTP handler over a store.
type Server struct {
	store   *store.Store
	version string
	// origin is the instance's public base URL. It builds the protected-resource
	// metadata URL a 401 points a client at, which is what kicks off OAuth
	// discovery.
	origin string
	// uploadsDir and libraryRoot are the two roots a comic's stored path can be
	// relative to, exactly as in server.Config. Both are readable; only uploadsDir
	// is ever written to or deleted from.
	uploadsDir  string
	libraryRoot string
	// ocr recognises text for read_comic_pages. It is built once at startup
	// because that is where a bad DOWITCHER_TESSDATA has to be fatal, and it is
	// cheap to hold: the engine reads the training data and nothing else until a
	// call arrives, so no Tesseract module is compiled on a server that is never
	// asked for text. Nil disables the text format rather than panicking.
	ocr *ocr.Engine
}

// New returns an MCP server backed by st. version rides along in the server's
// advertised implementation info; origin is the instance's public base URL,
// used to advertise where the OAuth flow starts; uploadsDir and libraryRoot
// resolve a comic row to a file; engine recognises page text, and may be nil.
func New(st *store.Store, version, origin, uploadsDir, libraryRoot string, engine *ocr.Engine) *Server {
	return &Server{
		store:       st,
		version:     version,
		origin:      origin,
		uploadsDir:  uploadsDir,
		libraryRoot: libraryRoot,
		ocr:         engine,
	}
}

// Handler is the http.Handler to mount (at /mcp). It wraps the streamable-HTTP
// MCP transport in bearer-token auth: a request without a valid access token
// gets 401 before it reaches any tool. The 401 carries a WWW-Authenticate header
// pointing at the protected-resource metadata, which is how an OAuth client
// discovers where to authenticate.
func (s *Server) Handler() http.Handler {
	srv := s.build()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	opts := &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: s.origin + "/.well-known/oauth-protected-resource",
	}
	return sdkauth.RequireBearerToken(s.verify, opts)(h)
}

// verify resolves the presented bearer to a user via the OAuth access-token
// store. The whole api.User rides in Extra so tool handlers get the admin flag
// without a second lookup. A token that does not resolve is rejected as
// ErrInvalidToken, which the middleware turns into a 401. Expiration is the
// token's real stored expiry: the middleware re-runs verify on every request,
// so a revoked or expired token stops resolving on the caller's next call.
func (s *Server) verify(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	u, expiresAt, err := s.store.AccessTokenUser(auth.HashToken(token))
	if err != nil {
		return nil, fmt.Errorf("unknown access token: %w", sdkauth.ErrInvalidToken)
	}
	return &sdkauth.TokenInfo{
		UserID:     u.ID,
		Expiration: time.Unix(expiresAt, 0),
		Extra:      map[string]any{userKey: u},
	}, nil
}

const userKey = "dowitcher_user"

// callerFrom recovers the authenticated user the verifier stashed. The bearer
// middleware always runs first, so a missing user here is a programming error,
// not an unauthenticated call.
func callerFrom(ctx context.Context) (api.User, bool) {
	ti := sdkauth.TokenInfoFromContext(ctx)
	if ti == nil {
		return api.User{}, false
	}
	u, ok := ti.Extra[userKey].(api.User)
	return u, ok
}

func (s *Server) build() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "dowitcher",
		Title:   "Dowitcher library",
		Version: s.version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_comics",
		Description: "List and search the comics visible to you. Filter by title/series text, a tag of yours, an exact series name, or a collection id; all filters are optional and combine with AND, and with none you get the whole library. Results are ordered by series, then issue number, then newest first, except when you filter by a collection, where the collection's own order is used. Page through with offset and limit.",
	}, s.listComics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_comic",
		Description: "Get one comic by id, including your own tags on it.",
	}, s.getComic)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_comic_pages",
		Description: "Read the contents of selected pages of a comic. Page numbers are 1-based, matching the pageCount get_comic reports. Choose the pages with pages (a list of individual page numbers), with fromPage and toPage (an inclusive range, both required together), or with both, in which case you get the union. Set format='image' to get the pages themselves as pictures, at most 10 per call and scaled to 1600px wide; set format='text' to get the lettering read off them by OCR, at most 5 per call. There is no default format, and asking for more pages than the limit is refused rather than truncated, so read a long stretch in several calls. OCR is imperfect on stylised lettering and gives you no idea what is drawn, so use format='image' unless you only need the words.",
	}, s.readComicPages)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "rename_comic",
		Description: "Set a comic's display title. Only the owner of an upload or claim, or an admin, can rename. The new title survives library rescans.",
	}, s.renameComic)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_download_link",
		Description: "Get a link that downloads a comic's CBZ file. The link works for one hour, needs no login, and covers only that one comic — treat it as a password and give it only to whoever should have the file.",
	}, s.createDownloadLink)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_tags",
		Description: "List your own tags with how many visible comics carry each. Tags are private to you.",
	}, s.listTags)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tag_comics",
		Description: "Retag one or more comics in a single call: every name in add is added to each comic, every name in remove is taken off it. Tags not in either list are left alone, and new tag names are created automatically. Tags are private to you and never affect what anyone else sees. Comics you cannot see come back in skipped instead of failing the call.",
	}, s.tagComics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_collections",
		Description: "List collections and reading lists visible to you: your own plus any another user has shared. Pass kind='collection' or kind='readinglist' to see only one.",
	}, s.listCollections)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_collection",
		Description: "Create a collection or reading list you own. Pass kind='readinglist' for an ordered reading list, otherwise a plain collection is made. Shared ones are readable by every user on the server; private ones (the default) are yours alone.",
	}, s.createCollection)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_collection",
		Description: "Edit one of your collections or reading lists: rename it, change its description, share/unshare it, or pick which comic's cover represents it. Only the fields you pass change. Without a cover pick, the first comic in order is used.",
	}, s.updateCollection)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_collection",
		Description: "Delete one of your collections or reading lists. The comics in it are untouched; only the grouping is removed.",
	}, s.deleteCollection)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "edit_collection_comics",
		Description: "Add comics to and remove comics from one of your own collections or reading lists in a single call. Added comics are appended in the order you list them, so passing a reading order here is enough — no reorder afterwards. Comics you cannot see, or that were not in the collection, come back in skipped instead of failing the call.",
	}, s.editCollectionComics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "reorder_collection",
		Description: "Set the order of a collection or reading list by passing all of its comic ids in the order you want. This is how a reading list gets its reading order.",
	}, s.reorderCollection)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_comic",
		Description: "Admin only. Claim a library comic: it leaves every other user's view and becomes yours, without moving the file. Only comics that came from the watched library folder can be claimed.",
	}, s.claimComic)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "set_comic_hidden",
		Description: "Admin only. With hidden=true, soft-delete a comic: it drops out of every listing, search and collection for every user, while its file, its tags and everyone's reading position are left untouched. This is what to use on a comic delete_comic refuses, such as a duplicate sitting in the read-only library folder. With hidden=false it goes back on the shelf intact; list_hidden_comics is how you find one to restore.",
	}, s.setComicHidden)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_hidden_comics",
		Description: "Admin only. List the comics that have been hidden. This is the only listing that returns them, so it is the only way to find one in order to unhide it.",
	}, s.listHiddenComics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_comic",
		Description: "Permanently delete a comic and its CBZ file, along with everyone's tags and reading position on it. Only your own uploads can be deleted (an admin can delete any upload), plus, for admins, comics converted from a PDF or archive dropped in the library folder. Comics the library scanner manages cannot be deleted here — remove those from the library folder instead. This cannot be undone, so confirm with the user before calling it.",
	}, s.deleteComic)

	return srv
}
