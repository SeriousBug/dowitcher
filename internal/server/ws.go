package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/coder/websocket"
)

// Hub fans out server→client messages to connected WebSocket clients, either to
// everyone (Broadcast) or to one user's sockets (BroadcastTo).
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}

	// last is the state replayed to a client the moment it connects, keyed by
	// message type.
	//
	// It has no per-user dimension, and that is the hazard worth naming: what
	// one broadcast puts in here is handed verbatim to whoever connects next,
	// whether or not they were allowed to see it. So only message types that are
	// identical for every user may be cached — cacheable is that gate, and
	// BroadcastTo deliberately never writes here at all.
	last map[api.WSType][]byte
}

type wsClient struct {
	// send is buffered so a broadcast never blocks on a client. 32 is deep
	// enough to ride out a scan burst and shallow enough that a client which
	// stopped reading is noticed rather than accumulating memory forever.
	send   chan []byte
	userID string
}

func newHub() *Hub {
	return &Hub{
		clients: map[*wsClient]struct{}{},
		last:    map[api.WSType][]byte{},
	}
}

// Broadcast sends a message to every connected client. It is for state that is
// the same for everyone; anything shaped by who is asking must go through
// BroadcastTo instead.
//
// It marshals once for all clients, and it snapshots the client set under the
// lock and releases it before writing: sending while holding the lock would let
// one slow reader stall every producer in the process. A client whose buffer is
// full is skipped rather than waited for — dropping a frame from a stream of
// state updates is harmless because the next one supersedes it.
func (h *Hub) Broadcast(msg api.WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	if cacheable(msg.Type) {
		h.last[msg.Type] = data
	}
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		select {
		case c.send <- data:
		default:
		}
	}
}

// BroadcastTo sends a message to one user's connected clients, skipping every
// other socket. It is how anything scoped to a user reaches the browser.
//
// A user may have several tabs open, so this is still a fan-out; the userID is a
// filter, not an address. It never touches the replay cache: the cache is keyed
// by message type alone, so parking a per-user payload there would hand it to
// the next client to connect regardless of who that is. Per-user state that
// needs replaying gets it from a per-user snapshot instead — see replayJobs.
//
// An empty userID matches nobody rather than everybody. A message with no owner
// is a bug in the caller, and fanning it out to the whole server is the one
// outcome worth ruling out.
func (h *Hub) BroadcastTo(userID string, msg api.WSMessage) {
	if userID == "" {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	clients := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		if c.userID != userID {
			continue
		}
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		select {
		case c.send <- data:
		default:
		}
	}
}

// cacheable reports whether a broadcast may be replayed to a client that
// connects later.
//
// The bar is not "is this state rather than an event" but the stricter "is this
// identical for every user", because the cache Broadcast writes to is shared by
// every future socket. Library scan progress is a property of the server, so it
// passes. A job event describes a moment and would be a lie once replayed, and
// anything filtered by visibility would be a leak — there is deliberately no
// entry here for api.WSTypeComics, and its declaration says why.
func cacheable(t api.WSType) bool { return t == api.WSTypeLibrary }

func (h *Hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	replay := make([][]byte, 0, len(h.last))
	for _, d := range h.last {
		replay = append(replay, d)
	}
	h.mu.Unlock()
	for _, d := range replay {
		select {
		case c.send <- d:
		default:
		}
	}
}

func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// replayJobs sends the import job picture to a freshly connected client: what is
// in flight now, and what finished while it was away. Jobs are events rather
// than state, so the hub's last-message cache cannot stand in for them.
//
// It is sent as one complete set rather than a delta. That is what lets a
// reconnecting client tell what is *not* running any more and clear the spinner
// for an import that finished, or died with the process, while it was
// disconnected. With a delta the spinner would outlive the job forever.
func (s *Server) replayJobs(c *wsClient) {
	data, err := json.Marshal(api.WSMessage{Type: api.WSTypeJobs, Jobs: s.jobSnapshot(c.userID)})
	if err != nil {
		return
	}
	select {
	case c.send <- data:
	default:
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	u, authed := s.currentUser(r)
	if !authed {
		writeErr(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	// The WS handshake is not subject to the same-origin policy, so the Origin
	// header is the only thing standing between a session cookie and any page on
	// the internet opening an authenticated socket to this server. Accepting "*"
	// would hand every visited site a live feed of the library, so the
	// configured origin is the allowlist.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{originHost(s.cfg.Origin)},
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	client := &wsClient{send: make(chan []byte, 32), userID: u.user.ID}
	s.hub.add(client)
	defer s.hub.remove(client)
	// A page loaded in the middle of an import would otherwise show nothing
	// until the next progress message, and nothing at all if the import is
	// stuck.
	s.replayJobs(client)

	ctx := r.Context()

	// Reader: drain client messages / detect disconnect.
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	}()

	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-client.send:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// originHost reduces the configured origin to the host:port that
// AcceptOptions.OriginPatterns matches against. A malformed origin yields a
// pattern nothing can match, which fails closed.
func originHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return strings.TrimSpace(origin)
	}
	return u.Host
}
