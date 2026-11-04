package api

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/auth"
	"github.com/noixdev/pulsix/internal/backup"
)

// registerBackupDownloadRoute mounts GET /api/v1/admin/backups/{name}/download
// on the root router. It is not a huma operation, so it is not in the
// OpenAPI document (and so not in the generated SPA client): huma encodes a
// response from a value, and a backup is a stream of up to gigabytes that
// has to reach the browser without being held in memory. The SPA links to it
// as a plain download.
//
// It is guarded like the huma routes next to it: the cookie middleware on
// the root router resolves the session, requireInstanceAdmin then demands
// an MFA-complete, non-token instance admin (the bearer middleware is never
// mounted here, so an Authorization header grants nothing), and the call is
// audited as downloadBackup with the same instance-level scope the huma
// routes use.
func registerBackupDownloadRoute(r chi.Router, deps Deps) {
	r.Get("/api/v1/admin/backups/{name}/download", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		p, err := requireInstanceAdmin(ctx)
		if err != nil {
			writeGateRefusal(w, err)
			return
		}
		name := chi.URLParam(req, "name")
		if !validBackupName(name) {
			http.Error(w, "no such backup", http.StatusNotFound)
			return
		}
		rc, size, err := deps.Backups.Open(ctx, name)
		if err != nil {
			if errors.Is(err, backup.ErrNotFound) || errors.Is(err, backup.ErrObjectNotFound) {
				http.Error(w, "no such backup", http.StatusNotFound)
				return
			}
			slog.ErrorContext(ctx, "open backup for download", "err", err, "name", name)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer func() { _ = rc.Close() }()

		recordBackupDownload(deps, req, p, name)

		h := w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		h.Set("Content-Length", strconv.FormatInt(size, 10))
		h.Set("Cache-Control", "no-store")
		if _, err := io.Copy(w, rc); err != nil {
			// The status line is gone; the short body (Content-Length)
			// tells the client the download is incomplete.
			slog.WarnContext(ctx, "backup download interrupted", "err", err, "name", name)
		}
	})
}

// recordBackupDownload writes the audit event the huma routes get from
// auditMiddleware, with the same action naming ("GET downloadBackup") and
// source address. A failure to record is logged, never shown: the download
// is already authorized.
func recordBackupDownload(deps Deps, req *http.Request, p auth.Principal, name string) {
	ctx := req.Context()
	meta := map[string]any{}
	if ip := deps.ClientIP.Resolve(req.RemoteAddr, req.Header.Values("X-Forwarded-For")); ip.IsValid() {
		meta["source_ip"] = ip.String()
	}
	if err := deps.Audit.Record(ctx, audit.Event{
		ActorID: &p.UserID, ActorKind: actorKind(ctx),
		Action: "GET downloadBackup", ObjectType: "backup", ObjectID: name,
		Summary: "downloaded a backup", Meta: meta,
	}); err != nil {
		slog.ErrorContext(ctx, "audit: failed to record event", "err", err, "action", "GET downloadBackup")
	}
}
