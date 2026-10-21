package server

import "net/http"

// handleDAV serves CalDAV (filled in by the CalDAV server).
func (s *Server) handleDAV(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "CalDAV is not ready yet")
}
