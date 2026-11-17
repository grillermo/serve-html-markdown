package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

// assetCacheControl is what Propshaft sends for digested assets: the name
// changes with the content, so a copy may be kept forever.
const assetCacheControl = "public, max-age=31536000, immutable"

// asset is one embedded file under its digested name.
type asset struct {
	data  []byte
	ctype string
	etag  string
}

// assetTable maps logical names ("expand.js") to digested public names
// ("expand-1a2b3c4d.js") and back. It is built once in New and only read
// afterwards, so requests share it without locking.
type assetTable struct {
	public map[string]string // logical name -> digested name
	byName map[string]*asset // digested name -> file
}

// newAssetTable reads every file of fsys's assets/ directory and names it
// <stem>-<first 8 hex of SHA-256><ext>. It panics if the directory cannot be
// read or has no files, on a subdirectory, and on an unreadable file: assets
// are flat. Names starting with "." or "_" (.DS_Store, .gitkeep) are skipped.
//
// The stylesheets reference no other asset (no url() or @import), so, unlike
// Propshaft, nothing is rewritten; TestEmbeddedStylesheetsHaveNoAssetReferences
// fails if that ever changes.
func newAssetTable(fsys fs.FS) *assetTable {
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		panic(fmt.Sprintf("web: embedded assets/ is unreadable: %v", err))
	}
	t := &assetTable{public: make(map[string]string, len(entries)), byName: make(map[string]*asset, len(entries))}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		if e.IsDir() {
			panic(fmt.Sprintf("web: assets/%s: subdirectories are not supported", e.Name()))
		}
		data, err := fs.ReadFile(fsys, "assets/"+e.Name())
		if err != nil {
			panic(fmt.Sprintf("web: read asset %q: %v", e.Name(), err))
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])[:8]
		ext := path.Ext(e.Name())
		name := strings.TrimSuffix(e.Name(), ext) + "-" + digest + ext
		t.public[e.Name()] = name
		t.byName[name] = &asset{data: data, ctype: assetContentType(ext), etag: `"` + digest + `"`}
	}
	if len(t.byName) == 0 {
		panic("web: embedded assets/ has no files")
	}
	return t
}

func assetContentType(ext string) string {
	switch strings.ToLower(ext) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".woff2":
		return "font/woff2"
	}
	return "application/octet-stream"
}

// AssetPath returns the digested URL path of the asset with the given logical
// name, e.g. "/assets/expand-1a2b3c4d.js" for "expand.js".
//
// An unknown name returns the plain "/assets/<name>" (which answers 404) and
// logs an error, so a typo in a template breaks one link instead of turning
// every page into a 500. New panics if a name the shipped templates use is
// missing, and a test renders each page and fetches its asset links.
func (s *Server) AssetPath(name string) string {
	if digested, ok := s.digests.public[name]; ok {
		return "/assets/" + digested
	}
	slog.Error("unknown asset", "name", name)
	return "/assets/" + name
}

// assets serves GET /assets/{name}. Only exact digested names match: the plain
// name, a wrong digest or anything with a path separator is a 404. The ETag is
// the digest, so http.ServeContent answers If-None-Match with 304, HEAD with
// headers only, and sets Content-Length.
func (s *Server) assets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		a := s.digests.byName[name]
		if a == nil { // a name with a slash, a backslash or ".." is never a key, so it lands here
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.ctype)
		h.Set("Cache-Control", assetCacheControl)
		h.Set("ETag", a.etag)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(a.data))
	})
}
