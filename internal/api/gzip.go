package api

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// withGzip compresses JSON answers and the web app's text assets for clients
// that accept it. Lectern's lists are repetitive JSON (the session list with
// ended sessions is ~400 KB, about 30 KB gzipped), and a phone on a mobile
// link waited for every byte before it could draw the home screen.
//
// Streams are left alone: event streams and websocket upgrades are never
// wrapped, and the writer decides per response, from its Content-Type, so a
// download or anything else that is not text passes straight through.
func withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" ||
			strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		gw := &gzipWriter{ResponseWriter: w, head: r.Method == http.MethodHead}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

var gzipPool = sync.Pool{New: func() any {
	z, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	return z
}}

type gzipWriter struct {
	http.ResponseWriter
	head    bool
	decided bool
	z       *gzip.Writer
}

func compressible(contentType string) bool {
	for _, prefix := range []string{"application/json", "text/html", "text/css", "text/javascript", "application/javascript", "image/svg+xml", "application/manifest+json"} {
		if strings.HasPrefix(contentType, prefix) {
			return true
		}
	}
	return false
}

func (g *gzipWriter) WriteHeader(status int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		if !g.head && status == http.StatusOK && h.Get("Content-Encoding") == "" && compressible(h.Get("Content-Type")) {
			h.Del("Content-Length")
			h.Set("Content-Encoding", "gzip")
			g.z = gzipPool.Get().(*gzip.Writer)
			g.z.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.z != nil {
		return g.z.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.z != nil {
		g.z.Flush()
	}
	http.NewResponseController(g.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection underneath.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) close() {
	if g.z == nil {
		return
	}
	g.z.Close()
	g.z.Reset(nil)
	gzipPool.Put(g.z)
	g.z = nil
}
