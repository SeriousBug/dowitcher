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
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
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
}

// New returns an MCP server backed by st. version rides along in the server's
// advertised implementation info; origin is the instance's public base URL,
// used to advertise where the OAuth flow starts.
func New(st *store.Store, version, origin string) *Server {
	return &Server{store: st, version: version, origin: origin}
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
		Description: "List comics visible to you, newest first. Use offset and limit to page through a large library.",
	}, s.listComics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_comics",
		Description: "Search the comics visible to you by title/series text, a tag, an exact series name, or a collection id. All filters are optional and combine with AND.",
	}, s.searchComics)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_comic",
		Description: "Get one comic by id, including your own tags on it.",
	}, s.getComic)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_tags",
		Description: "List your own tags with how many visible comics carry each. Tags are private to you.",
	}, s.listTags)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "tag_comic",
		Description: "Add one or more tags to a comic. New tag names are created automatically. Tags are private to you and never affect what anyone else sees. Existing tags on the comic are kept.",
	}, s.tagComic)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "untag_comic",
		Description: "Remove one or more of your tags from a comic. Tags not currently on the comic are ignored.",
	}, s.untagComic)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_collections",
		Description: "List collections visible to you: your own plus any another user has shared.",
	}, s.listCollections)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_collection",
		Description: "Create a new collection you own. Shared collections are readable by every user on the server; private ones (the default) are yours alone.",
	}, s.createCollection)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "add_to_collection",
		Description: "Add a comic to one of your own collections. You must own the collection and be able to see the comic.",
	}, s.addToCollection)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "claim_comic",
		Description: "Admin only. Claim a library comic: it leaves every other user's view and becomes yours, without moving the file. Only comics that came from the watched library folder can be claimed.",
	}, s.claimComic)

	return srv
}
