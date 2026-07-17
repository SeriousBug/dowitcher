// Command longbox is a self-hosted comic reader with passkey-only auth.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/SeriousBug/longbox/internal/auth"
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

	// Read the bypass before anything else: if it is set on an https origin the
	// process must die here, not after it has opened a listener.
	devAuth, err := auth.DevAuthFromEnv(origin)
	if err != nil {
		log.Fatalf("%v", err)
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

	srv := server.New(st, authMgr, server.Config{
		RPID:    rpID,
		Origin:  origin,
		Secure:  strings.HasPrefix(origin, "https://"),
		DevAuth: devAuth,
	})
	log.Printf("library root %s, data dir %s", libraryRoot, dataDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
