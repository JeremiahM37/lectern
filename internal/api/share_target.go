package api

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The installed web app is a share target: "Share" -> Lectern, from any
// Android app or the desktop, lands here. The browser delivers the shared
// image in a POST navigation; it is held briefly so /share.html can offer a
// session to put it in (upload, mirror to the session's clipboard and type its
// path into the prompt, exactly like a paste). docs/clipboard.md.

const (
	shareTTL     = 10 * time.Minute
	shareMax     = 8
	shareMaxSize = 20 << 20
)

type sharedItem struct {
	Mime, Name, Text string
	Data             []byte
	At               time.Time
}

type shareStash struct {
	mu    sync.Mutex
	items map[string]*sharedItem
}

func (st *shareStash) put(it *sharedItem) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.items == nil {
		st.items = map[string]*sharedItem{}
	}
	now := time.Now()
	oldest, oldestID := now, ""
	for id, v := range st.items {
		if now.Sub(v.At) > shareTTL {
			delete(st.items, id)
		} else if v.At.Before(oldest) {
			oldest, oldestID = v.At, id
		}
	}
	if len(st.items) >= shareMax && oldestID != "" {
		delete(st.items, oldestID)
	}
	var b [16]byte
	rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	it.At = now
	st.items[id] = it
	return id
}

func (st *shareStash) get(id string) *sharedItem {
	st.mu.Lock()
	defer st.mu.Unlock()
	it := st.items[id]
	if it == nil || time.Since(it.At) > shareTTL {
		delete(st.items, id)
		return nil
	}
	return it
}

// shareTarget is POST /share-target.
func (s *Server) shareTarget(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Sharing to Lectern") {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, shareMaxSize+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		httpError(w, 400, "could not read the shared content")
		return
	}
	defer r.MultipartForm.RemoveAll()
	it := &sharedItem{Text: strings.TrimSpace(strings.Join([]string{r.FormValue("title"), r.FormValue("text"), r.FormValue("url")}, "\n"))}
	if fhs := r.MultipartForm.File["image"]; len(fhs) > 0 {
		f, err := fhs[0].Open()
		if err == nil {
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, shareMaxSize+1))
			if err == nil && len(data) <= shareMaxSize && len(data) > 0 {
				mime := strings.ToLower(strings.Split(fhs[0].Header.Get("Content-Type"), ";")[0])
				if !mirrorTypes[mime] || mime == "text/plain" {
					// the browser may send a generic type: trust the bytes
					mime = http.DetectContentType(data)
				}
				if strings.HasPrefix(mime, "image/") {
					it.Mime, it.Data, it.Name = mime, data, fhs[0].Filename
				}
			}
		}
	}
	if it.Data == nil && it.Text == "" {
		httpError(w, 400, "nothing to share")
		return
	}
	http.Redirect(w, r, "/share.html?id="+s.shares.put(it), http.StatusSeeOther)
}

// shareStashGet is GET /api/share-stash/{id}: the shared item, for the picker
// page. {"text":...} for text, else the image bytes.
func (s *Server) shareStashGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireHuman(w, r, "Reading a shared item") {
		return
	}
	it := s.shares.get(r.PathValue("id"))
	if it == nil {
		httpError(w, 404, "this share expired; share it again")
		return
	}
	if r.URL.Query().Get("meta") != "" || it.Data == nil {
		writeJSON(w, 200, map[string]any{"text": it.Text, "mime": it.Mime, "name": it.Name, "size": len(it.Data)})
		return
	}
	w.Header().Set("Content-Type", it.Mime)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(it.Data)
}
