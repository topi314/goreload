package goreload

import (
	"fmt"
	"net/http"
)

// CacheMiddleware is a middleware that sets Cache-Control headers to enable caching for the specified max age.
func (r *Reloader) CacheMiddleware(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		if !r.Enabled() {
			handler.ServeHTTP(w, rq)
			return
		}

		w.Header().Set("Cache-Control", fmt.Sprintf("stale-while-revalidate, max-age=%d", r.maxAge))
		handler.ServeHTTP(w, rq)
	})
}
