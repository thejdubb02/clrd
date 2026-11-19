package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

// publishMultipart reads a native upload: a "notification" part holding the
// JSON, then a "file" part streamed to disk. The JSON comes first so the
// token and topic are checked before any file byte is stored.
func (s *Server) publishMultipart(r *http.Request, tok *Token) (*Notification, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errf(400, "bad_request", "bad multipart body: %v", err)
	}
	part, err := mr.NextPart()
	if err != nil || part.FormName() != "notification" {
		return nil, errf(400, "bad_request", "first part must be named notification")
	}
	var in PublishInput
	if err := json.NewDecoder(http.MaxBytesReader(nil, part, maxBody)).Decode(&in); err != nil {
		return nil, errf(400, "bad_request", "bad notification JSON: %v", err)
	}
	if part, err = mr.NextPart(); err != nil || part.FormName() != "file" {
		return nil, errf(400, "bad_request", "second part must be named file")
	}
	return s.PublishFile(r.Context(), tok, in, part.FileName(), part)
}

// inline types a browser may show in place; anything else downloads.
var inline = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// serveFile: GET /api/v1/files/{id}, read scope on the notification's topic.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, tok *Token) error {
	id := r.PathValue("id")
	gone := errf(404, "not_found", "file not found")
	if !fileID.MatchString(id) {
		return gone
	}
	n, err := getNotification(r.Context(), s.db, `file = ? AND file != ''`, id) // second clause lets SQLite use the partial index
	if isNotFound(err) {
		return gone
	}
	if err != nil {
		return err
	}
	if err := tok.need(ScopeRead, n.Topic); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(s.opt.Attachments, id))
	if errors.Is(err, fs.ErrNotExist) {
		return gone
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	typ, disp := "application/octet-stream", "attachment"
	if inline[n.Attachment.Type] {
		typ, disp = n.Attachment.Type, "inline"
	}
	if cd := mime.FormatMediaType(disp, map[string]string{"filename": n.Attachment.Name}); cd != "" {
		disp = cd
	}
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("Content-Disposition", disp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "private, no-cache") // revalidate, so a lost token stops a cached copy
	http.ServeContent(w, r, "", info.ModTime(), f)
	return nil
}
