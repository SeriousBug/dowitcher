// Command longbox is a self-hosted comic reader with passkey-only auth.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/auth"
	"github.com/SeriousBug/longbox/internal/imports"
	"github.com/SeriousBug/longbox/internal/library"
	"github.com/SeriousBug/longbox/internal/server"
	"github.com/SeriousBug/longbox/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("longbox ")

	dbPath := env("LONGBOX_DB", "/data/longbox.db")
	addr := env("LONGBOX_ADDR", ":8080")
	origin := env("LONGBOX_ORIGIN", "http://localhost:8080")
	rpID := env("LONGBOX_RP_ID", "localhost")
	libraryRoot := env("LONGBOX_LIBRARY", "/library")
	dataDir := env("LONGBOX_DATA", "/data")

	// A rescan of an unchanged library costs microseconds — the content hash
	// reads the zip's central directory, not its contents — so the sweep is
	// cheap enough to run often. It exists to catch what fsnotify misses: a
	// dropped event, an NFS mount, a change made while the process was down.
	sweepInterval := 15 * time.Minute
	if v := os.Getenv("LONGBOX_SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("LONGBOX_SWEEP_INTERVAL: %v", err)
		}
		sweepInterval = d
	}

	// Read the bypass before anything else: if it is set somewhere it must not
	// be, the process must die here, not after it has opened a listener. In a
	// build without -tags dev there is no bypass to read and this always yields
	// nil, which is the point of the tag.
	devAuth, err := auth.DevAuthFromEnv(origin, addr)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if devAuth == nil && auth.DevAuthRequested() {
		log.Printf("%s is set, but this binary was not built with -tags dev; "+
			"authentication stays ON", auth.DevAuthEnv)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Recovery: `longbox invite [--normal]` mints a fresh enrollment link from
	// the host and exits. This is the account-recovery path if every passkey is
	// lost — host access already equals full control over the database.
	if len(os.Args) > 1 && os.Args[1] == "invite" {
		mintInvite(st, origin, os.Args[2:])
		return
	}

	authMgr, err := auth.NewManager(st, auth.Config{RPID: rpID, Origin: origin})
	if err != nil {
		log.Fatalf("auth manager: %v", err)
	}

	if url, err := auth.Bootstrap(st, origin); err != nil {
		log.Fatalf("bootstrap: %v", err)
	} else if url != "" {
		log.Printf("no users yet — enroll the first admin passkey here:\n\n    %s\n", url)
	}

	if devAuth != nil {
		log.Print(devAuth.Banner())
	}

	uploadsDir := filepath.Join(dataDir, "uploads")
	coverCacheDir := filepath.Join(dataDir, "covers")
	importTempDir := filepath.Join(dataDir, "tmp")

	// Create the staging dir up front rather than lazily on first use: the only
	// thing that would otherwise create it is an upload, so a fresh install's
	// first import fails and every later one succeeds. Failing here instead
	// surfaces a bad data volume at boot, where it is diagnosable.
	if err := os.MkdirAll(importTempDir, 0o755); err != nil {
		log.Fatalf("create import staging dir: %v", err)
	}

	srv := server.New(st, authMgr, server.Config{
		RPID:    rpID,
		Origin:  origin,
		Secure:  strings.HasPrefix(origin, "https://"),
		DevAuth: devAuth,

		LibraryRoot:   libraryRoot,
		UploadsDir:    uploadsDir,
		CoverCacheDir: coverCacheDir,
		// Imports stage a whole folder of images before packing, which routinely
		// runs to gigabytes. Keeping the staging area under the data volume
		// rather than the OS temp dir avoids filling a small tmpfs mid-import.
		ImportTempDir: importTempDir,
	})
	log.Printf("library root %s, data dir %s", libraryRoot, dataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Constructing the manager is also the orphan-job sweep: a job still marked
	// running in the DB is a lie left by a crash, since its goroutine died with
	// the process.
	im, err := imports.NewManager(st, srv.Hub(), imports.ManagerConfig{
		UploadsDir: uploadsDir,
		ReportDir:  filepath.Join(dataDir, "imports"),
	})
	if err != nil {
		log.Fatalf("import manager: %v", err)
	}
	srv.SetImporter(im)

	lib := library.New(st, library.Config{
		Root:          libraryRoot,
		DataDir:       dataDir,
		SweepInterval: sweepInterval,
	}, func(s api.LibraryStatus) {
		srv.Hub().Broadcast(api.WSMessage{Type: api.WSTypeLibrary, Library: &s})
	})
	srv.SetLibrary(lib)

	// Scan before serving so a fresh instance comes up populated. A missing or
	// empty root is not fatal: the server still has to come up to serve the
	// enrollment link and let someone fix the mount.
	if err := lib.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("warning: initial scan: %v", err)
	}
	go lib.Run(ctx)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
		// Bounded so a client that opens a connection and never finishes its
		// headers cannot hold one open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdownCtx)
	log.Printf("shutdown complete")
}

// mintInvite prints a fresh enrollment link. It defaults to admin because the
// subcommand exists for the case where nobody can get in at all.
func mintInvite(st *store.Store, origin string, args []string) {
	isAdmin := true
	for _, a := range args {
		if a == "--normal" {
			isAdmin = false
		}
	}
	token, _, err := auth.NewInvite(st, "", "", isAdmin)
	if err != nil {
		log.Fatalf("mint invite: %v", err)
	}
	kind := "admin"
	if !isAdmin {
		kind = "normal"
	}
	log.Printf("%s enrollment link (valid 24h, single use):\n\n    %s\n", kind, auth.InviteURL(origin, token))
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
