package app

import (
	"github.com/bnema/neferwl/internal/adapters/wayland"
	"github.com/bnema/neferwl/internal/ports"
)

// waylandContent is the contentAttacher of a wayland server.
type waylandContent struct{ server *wayland.Server }

func (w waylandContent) Attach(name string) (<-chan *ports.ContentBatch, ports.ContentReturn) {
	return w.server.AttachOutput(name)
}

func (w waylandContent) Detach(name string) { w.server.DetachOutput(name) }
