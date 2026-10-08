// Package core is Godwit's mail engine.
package core

import (
	"context"
	"os"
	"path/filepath"

	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/store"
)

// Options configures a Service.
type Options struct {
	DataDir  string
	AutoSync bool // background workers arrive with the worker task; unused for now
}

// Service owns the database and event bus of one Godwit data directory.
type Service struct {
	db      *store.DB
	bus     *events.Bus
	dataDir string
	opts    Options
}

// New creates DataDir if needed and opens DataDir/godwit.db.
func New(ctx context.Context, opts Options) (*Service, error) {
	if err := os.MkdirAll(opts.DataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := store.Open(ctx, filepath.Join(opts.DataDir, "godwit.db"))
	if err != nil {
		return nil, err
	}
	return &Service{db: db, bus: events.New(), dataDir: opts.DataDir, opts: opts}, nil
}

// Close releases the database.
func (s *Service) Close() error { return s.db.Close() }

// Subscribe returns UI notifications and a func that unsubscribes.
func (s *Service) Subscribe() (<-chan events.Event, func()) { return s.bus.Subscribe() }
