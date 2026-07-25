package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"webhook-gateway/internal/api/middleware"
	"webhook-gateway/internal/db"
	"webhook-gateway/internal/tenancy"
)

// Retry policy for an ephemeral tunnel destination. A tunnel is an interactive
// dev loop, not a production consumer: if the developer's local handler errors,
// they fix it and re-trigger, so a failed delivery is reported immediately
// instead of being retried for three days. The backoff columns are NOT NULL and
// unreachable at max_attempts=1, but are set to sane values anyway.
const (
	tunnelTimeoutMs          = 30000 // a local server can be slow to boot
	tunnelMaxAttempts        = 1
	tunnelBackoffBaseSeconds = 1
	tunnelBackoffMaxSeconds  = 5
)

// pingInterval keeps the socket alive through proxies and NAT that would
// otherwise drop an idle connection between webhooks.
const pingInterval = 30 * time.Second

type handler struct {
	q        *db.Queries
	registry *Registry
}

// Register mounts GET /api/tunnel and sweeps tunnel destinations orphaned by a
// previous run. The registry it is given must be the same one the delivery
// worker holds, or deliveries will never find the socket.
func Register(ctx context.Context, mux *http.ServeMux, q *db.Queries, authz *middleware.Auth, registry *Registry) error {
	if _, err := q.DeleteStaleTunnelDestinations(ctx, tenancy.DefaultTenantID); err != nil {
		return fmt.Errorf("sweeping stale tunnel destinations: %w", err)
	}
	h := &handler{q: q, registry: registry}
	mux.Handle("GET /api/tunnel", authz.RequireScope(middleware.ScopeTunnel, http.HandlerFunc(h.tunnel)))
	return nil
}

// tunnel upgrades the request and serves one CLI session for its lifetime.
func (h *handler) tunnel(w http.ResponseWriter, r *http.Request) {
	// Everything that can fail with a normal HTTP status happens before the
	// upgrade — afterwards the only way to report a problem is a close code.
	sources, err := h.resolveSources(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		writeError(w, err)
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("tunnel: websocket upgrade failed", "error", err)
		return
	}
	defer func() { _ = ws.CloseNow() }()

	// The session outlives the request context in one direction only: cleanup
	// must still run after the client vanishes, so deletes use their own ctx.
	ctx := r.Context()

	dest, err := h.attach(ctx)
	if err != nil {
		slog.Error("tunnel: attaching ephemeral destination", "error", err)
		_ = ws.Close(websocket.StatusInternalError, "could not attach tunnel")
		return
	}
	conn := newConn(ws)

	// Register before the routes exist, so no event can fan out to this
	// destination before the socket is reachable.
	h.registry.register(dest.ID.Bytes, conn)
	defer h.detach(dest)

	if err := h.routeSources(ctx, dest, sources); err != nil {
		slog.Error("tunnel: routing sources", "error", err)
		_ = ws.Close(websocket.StatusInternalError, "could not route sources")
		return
	}

	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name
	}
	if err := conn.write(ctx, ReadyFrame{
		Type:          FrameReady,
		DestinationID: uuidString(dest.ID),
		Sources:       names,
	}); err != nil {
		slog.Warn("tunnel: sending ready frame", "error", err)
		return
	}
	slog.Info("tunnel attached", "destination_id", uuidString(dest.ID), "sources", names)

	go h.keepalive(ctx, ws)

	// Blocks for the life of the session; any read error ends the tunnel.
	err = conn.readLoop(ctx)
	slog.Info("tunnel detached", "destination_id", uuidString(dest.ID), "reason", err)
}

// resolveSources maps the ?source= query parameter to the sources whose events
// should flow down this tunnel. Empty or "all" means every source.
func (h *handler) resolveSources(ctx context.Context, name string) ([]db.Source, error) {
	if name == "" || name == "all" {
		sources, err := h.q.ListSources(ctx, tenancy.DefaultTenantID)
		if err != nil {
			return nil, fmt.Errorf("listing sources: %w", err)
		}
		if len(sources) == 0 {
			return nil, errNoSources
		}
		return sources, nil
	}

	source, err := h.q.GetSourceByName(ctx, db.GetSourceByNameParams{
		TenantID: tenancy.DefaultTenantID,
		Name:     name,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errUnknownSource
		}
		return nil, fmt.Errorf("looking up source %q: %w", name, err)
	}
	return []db.Source{source}, nil
}

// attach creates the ephemeral destination backing this session. It is a normal
// destinations row, so fan-out, deliveries, attempts, and the trace view all
// work on a tunnel with no special-casing anywhere but dispatch.
func (h *handler) attach(ctx context.Context) (db.Destination, error) {
	label := "tunnel-" + randomLabel()
	return h.q.InsertDestination(ctx, db.InsertDestinationParams{
		TenantID:           tenancy.DefaultTenantID,
		Name:               label,
		Url:                URLScheme + label,
		AuthConfig:         []byte("{}"),
		TimeoutMs:          tunnelTimeoutMs,
		MaxAttempts:        tunnelMaxAttempts,
		BackoffBaseSeconds: tunnelBackoffBaseSeconds,
		BackoffMaxSeconds:  tunnelBackoffMaxSeconds,
	})
}

func (h *handler) routeSources(ctx context.Context, dest db.Destination, sources []db.Source) error {
	for _, source := range sources {
		if _, err := h.q.InsertRoute(ctx, db.InsertRouteParams{
			TenantID:      tenancy.DefaultTenantID,
			SourceID:      source.ID,
			DestinationID: dest.ID,
			Enabled:       true,
		}); err != nil {
			return err
		}
	}
	return nil
}

// detach removes the destination when the socket goes away. Deleting the row
// cascades to its routes and to any delivery still queued for it, so a closed
// tunnel leaves no retries chasing a socket that no longer exists.
func (h *handler) detach(dest db.Destination) {
	h.registry.unregister(dest.ID.Bytes)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := h.q.DeleteDestination(ctx, db.DeleteDestinationParams{
		ID:       dest.ID,
		TenantID: tenancy.DefaultTenantID,
	}); err != nil {
		slog.Error("tunnel: deleting ephemeral destination", "error", err, "destination_id", uuidString(dest.ID))
	}
}

func (h *handler) keepalive(ctx context.Context, ws *websocket.Conn) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Ping(pingCtx)
			cancel()
			if err != nil {
				// The read loop sees the same failure and ends the session.
				return
			}
		}
	}
}

// Sentinel errors that map to client-visible statuses before the upgrade.
var (
	errNoSources     = errors.New("no sources configured to tunnel")
	errUnknownSource = errors.New("unknown source")
)

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "internal error"
	switch {
	case errors.Is(err, errUnknownSource):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, errNoSources):
		status, message = http.StatusBadRequest, err.Error()
	default:
		slog.Error("tunnel: resolving sources", "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func randomLabel() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
