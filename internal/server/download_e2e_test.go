package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/oauth"
	"github.com/SeriousBug/dowitcher/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// downloadServer brings up a server with MCP enabled and real CBZs on disk: a
// server-wide library comic and one upload owned by bob.
func downloadServer(t *testing.T) (srv *Server, ts *httptest.Server, st *store.Store, lib, upload store.ComicRow, alice, bob string) {
	t.Helper()
	var cfg Config
	srv, ts, st, _ = newTestServer(t, func(c *Config) {
		dir := t.TempDir()
		c.MCPEnabled = true
		c.LibraryRoot = filepath.Join(dir, "library")
		c.UploadsDir = filepath.Join(dir, "uploads")
		c.CoverCacheDir = filepath.Join(dir, "covers")
		cfg = *c
	})
	for _, d := range []string{cfg.LibraryRoot, cfg.UploadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a, err := st.CreateUser(store.NewID(), "alice", true)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	b, err := st.CreateUser(store.NewID(), "bob", false)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	lib, _ = addComic(t, st, cfg.LibraryRoot, "Series/Public.cbz", 3, store.ComicRow{Title: "Public: One/Two"})
	upload, _ = addComic(t, st, cfg.UploadsDir, "bob/Private.cbz", 2, store.ComicRow{
		Title: "BobOnly", Source: store.SourceUpload, OwnerID: b.ID,
	})
	return srv, ts, st, lib, upload, a.ID, b.ID
}

// mcpToken mints an OAuth access token straight through the store, standing in
// for the browser flow the agent would otherwise run.
func mcpToken(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	clientID := store.NewID()
	if err := st.CreateOAuthClient(clientID, "agent", []string{"https://example.test/cb"}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	secret := oauth.NewToken()
	if err := st.CreateAccessToken(auth.HashToken(secret), clientID, userID, "mcp", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("mint access token: %v", err)
	}
	return secret
}

// mintDownloadLink calls the MCP tool as userID and returns the URL it hands back.
func mintDownloadLink(t *testing.T, ts *httptest.Server, st *store.Store, userID, comicID string) string {
	t.Helper()
	sess := mcpConnect(t, ts.URL, mcpToken(t, st, userID))
	res, err := sess.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "create_download_link",
		Arguments: map[string]any{"comicId": comicID},
	})
	if err != nil {
		t.Fatalf("create_download_link: %v", err)
	}
	if res.IsError {
		t.Fatalf("create_download_link errored: %+v", res.Content)
	}
	var out struct {
		URL       string `json:"url"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode link: %v", err)
	}
	if want := ts.URL + "/comics/" + comicID + "/download/"; !strings.HasPrefix(out.URL, want) {
		t.Fatalf("url = %q, want prefix %q", out.URL, want)
	}
	if d := time.Until(time.Unix(out.ExpiresAt, 0)); d > time.Hour || d < 55*time.Minute {
		t.Fatalf("link expires in %v, want about an hour", d)
	}
	return out.URL
}

// anon is a client with no session cookie at all: the download link has to work
// without one, since that is the whole point of handing it to someone.
func anon(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := (&http.Client{}).Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// TestDownloadLinkServesCBZ: the minted URL downloads the archive's bytes with
// no session behind the request.
func TestDownloadLinkServesCBZ(t *testing.T) {
	srv, ts, st, lib, _, alice, _ := downloadServer(t)
	link := mintDownloadLink(t, ts, st, alice, lib.ID)

	resp, body := anon(t, link)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: %d %s", resp.StatusCode, body)
	}
	onDisk, err := os.ReadFile(srv.comicFile(lib))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, onDisk) {
		t.Fatalf("downloaded %d bytes, want the %d bytes of the CBZ", len(body), len(onDisk))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.comicbook+zip" {
		t.Errorf("content-type = %q", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, ".cbz") {
		t.Errorf("content-disposition = %q, want an attachment named after the comic", cd)
	}
	// The title's slash must not survive into the filename.
	if strings.Contains(cd, "One/Two") {
		t.Errorf("content-disposition = %q, path separator leaked from the title", cd)
	}
}

// TestDownloadLinkRejectsExpiredToken: expiry is enforced by the lookup, so a
// token that has aged out reads as an invalid link.
func TestDownloadLinkRejectsExpiredToken(t *testing.T) {
	_, ts, st, lib, _, alice, _ := downloadServer(t)
	secret := oauth.NewToken()
	if err := st.CreateDownloadToken(auth.HashToken(secret), lib.ID, alice, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatalf("mint expired token: %v", err)
	}
	resp, _ := anon(t, ts.URL+"/comics/"+lib.ID+"/download/"+secret)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired link = %d, want 404", resp.StatusCode)
	}
}

// TestDownloadLinkIsBoundToOneComic: a token minted for one comic cannot be
// pointed at another by editing the id in the path.
func TestDownloadLinkIsBoundToOneComic(t *testing.T) {
	_, ts, st, lib, upload, _, bob := downloadServer(t)
	link := mintDownloadLink(t, ts, st, bob, upload.ID)
	token := link[strings.LastIndexByte(link, '/')+1:]

	resp, body := anon(t, ts.URL+"/comics/"+lib.ID+"/download/"+token)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("swapped comic id = %d, want 404", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("PK")) {
		t.Error("a token for another comic must not serve any archive bytes")
	}
}

// TestDownloadLinkRejectsComicNoLongerVisible: visibility is re-checked at
// redemption, so a library comic claimed out from under the minter stops
// downloading even though the token has not expired.
func TestDownloadLinkRejectsComicNoLongerVisible(t *testing.T) {
	_, ts, st, lib, _, alice, bob := downloadServer(t)
	link := mintDownloadLink(t, ts, st, bob, lib.ID)
	if resp, _ := anon(t, link); resp.StatusCode != http.StatusOK {
		t.Fatalf("link should work before the claim, got %d", resp.StatusCode)
	}

	if err := st.ClaimComic(alice, lib.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if resp, _ := anon(t, link); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("link after the comic left bob's view = %d, want 404", resp.StatusCode)
	}
}

// TestDownloadLinkNotMintedForInvisibleComic: minting runs the same visibility
// check, so alice cannot get a link to bob's upload in the first place.
func TestDownloadLinkNotMintedForInvisibleComic(t *testing.T) {
	_, ts, st, _, upload, alice, _ := downloadServer(t)
	sess := mcpConnect(t, ts.URL, mcpToken(t, st, alice))
	res, err := sess.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "create_download_link",
		Arguments: map[string]any{"comicId": upload.ID},
	})
	if err != nil {
		t.Fatalf("create_download_link: %v", err)
	}
	if !res.IsError {
		t.Fatal("minting a link for another user's upload should be refused")
	}
}
