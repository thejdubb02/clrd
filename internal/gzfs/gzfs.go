// Package gzfs reads files the packed build stores gzipped (#690). Every
// text file is stored once, as name.gz, by go run ./server/cmd/pack. Open
// returns the decoded file, so templates, fs.WalkDir and http.FileServerFS
// work as on a plain embed; Serve sends the stored bytes as they are to a
// client that takes gzip. Nothing holds both forms for longer than a request.
package gzfs

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// FS joins a tree of .gz files with a tree of raw ones. The two hold different
// top-level folders; the root lists the gz tree only.
type FS struct{ gz, raw fs.FS }

// New returns the files under dir in gz decoded, and raw's as they are.
// raw may be nil.
func New(gz fs.FS, dir string, raw fs.FS) FS {
	sub, _ := fs.Sub(gz, dir)
	return FS{sub, raw}
}

func (f FS) Open(name string) (fs.File, error) {
	if b, err := fs.ReadFile(f.gz, name+".gz"); err == nil {
		z, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		if b, err = io.ReadAll(z); err != nil {
			return nil, err
		}
		return &file{bytes.NewReader(b), info{path.Base(name), int64(len(b)), 0o444}}, nil
	}
	d, err := f.gz.Open(name)
	if err == nil {
		if st, _ := d.Stat(); st != nil && st.IsDir() {
			if rd, ok := d.(fs.ReadDirFile); ok {
				return dir{rd}, nil
			}
		}
		d.Close()
	}
	if f.raw == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f.raw.Open(name)
}

// Serve answers r with name when fsys stores it gzipped and the client takes
// gzip: the stored bytes go out as they are, with Content-Encoding: gzip.
// Otherwise it writes nothing and returns false, and the caller serves the
// decoded file. Content-Type comes from the extension, or a sniff of the
// decoded bytes, never of the gzip ones.
func Serve(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	f, ok := fsys.(FS)
	if !ok || !AcceptsGzip(strings.Join(r.Header.Values("Accept-Encoding"), ",")) {
		return false
	}
	b, err := fs.ReadFile(f.gz, name+".gz")
	if err != nil {
		return false
	}
	h := w.Header()
	if h.Get("Content-Type") == "" {
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			plain, _ := fs.ReadFile(f, name)
			ct = http.DetectContentType(plain)
		}
		h.Set("Content-Type", ct)
	}
	h.Set("Content-Encoding", "gzip")
	// A range of the gzip bytes is of no use to anyone: whole file only.
	r.Header.Del("Range")
	http.ServeContent(gzWriter{w, len(b)}, r, name, time.Time{}, bytes.NewReader(b))
	return true
}

// gzWriter gives a 200 the gzip length, which ServeContent leaves out once
// Content-Encoding is set, and takes the encoding off any bodiless answer
// such as a 412.
type gzWriter struct {
	http.ResponseWriter
	n int
}

func (g gzWriter) WriteHeader(code int) {
	if code == http.StatusOK {
		g.Header().Set("Content-Length", strconv.Itoa(g.n))
	} else {
		g.Header().Del("Content-Encoding")
	}
	g.ResponseWriter.WriteHeader(code)
}

// AcceptsGzip reports whether an Accept-Encoding header takes gzip. A
// q-value of 0 refuses it.
func AcceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		k, v, _ := strings.Cut(params, "=")
		if strings.TrimSpace(k) != "q" {
			return true
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return err == nil && q > 0
	}
	return false
}

type file struct {
	*bytes.Reader
	info
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }
func (*file) Close() error                 { return nil }

type info struct {
	name string
	size int64
	mode fs.FileMode
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return i.mode }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.mode.IsDir() }
func (i info) Sys() any           { return nil }

// dir lists a gz folder by the names its files are opened by.
type dir struct{ fs.ReadDirFile }

func (d dir) ReadDir(n int) ([]fs.DirEntry, error) {
	es, err := d.ReadDirFile.ReadDir(n)
	for i, e := range es {
		es[i] = entry{e}
	}
	return es, err
}

type entry struct{ fs.DirEntry }

func (e entry) Name() string { return strings.TrimSuffix(e.DirEntry.Name(), ".gz") }

// Info keeps the stored size: listings show names only.
func (e entry) Info() (fs.FileInfo, error) {
	i, err := e.DirEntry.Info()
	if err != nil {
		return nil, err
	}
	return info{e.Name(), i.Size(), i.Mode()}, nil
}
