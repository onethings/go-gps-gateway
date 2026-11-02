package api

import (
	"net/http"
)

// NamedCache is one cache the server cache route reports: the name the JSON
// object uses and a function returning the live entry count.
type NamedCache struct {
	Name string
	Size func() int
}

// WithGeoCaches publishes the sizes of the geographic caches at
// GET /api/server/cache. Without it the route reports an empty object.
func WithGeoCaches(caches ...NamedCache) Option {
	return func(s *Server) {
		for _, cache := range caches {
			if cache.Name != "" && cache.Size != nil {
				s.geoCaches = append(s.geoCaches, cache)
			}
		}
	}
}

// handleServerCache answers GET /api/server/cache with the entry count of every
// cache the deployment exposes. The Java route shows the same numbers on the
// server page and lets an operator confirm a cache is warming up.
func (s *Server) handleServerCache(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	counts := make(map[string]int, len(s.geoCaches))
	for _, cache := range s.geoCaches {
		counts[cache.Name] = cache.Size()
	}
	writeJSON(w, http.StatusOK, counts)
}
