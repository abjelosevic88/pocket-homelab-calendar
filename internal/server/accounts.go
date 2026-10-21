package server

import (
	"context"

	"github.com/go-chi/chi/v5"
)

// External calendar accounts (CalDAV, Google, ICS, Nextcloud through Core).

func (s *Server) accountRoutes(r chi.Router)       {}
func (s *Server) publicAccountRoutes(r chi.Router) {}
func (s *Server) syncLoop(ctx context.Context)     { <-ctx.Done() }
func (s *Server) loadSecretKey() error             { return nil }
