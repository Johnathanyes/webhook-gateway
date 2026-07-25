package tunnel

import "sync"

// Registry maps an ephemeral destination id to the live socket serving it.
type Registry struct {
	mu    sync.RWMutex
	conns map[[16]byte]*Conn
}

func NewRegistry() *Registry {
	return &Registry{conns: make(map[[16]byte]*Conn)}
}

// Lookup returns the connection serving destID, if one is attached.
func (r *Registry) Lookup(destID [16]byte) (*Conn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[destID]
	return c, ok
}

func (r *Registry) register(destID [16]byte, c *Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns[destID] = c
}

func (r *Registry) unregister(destID [16]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, destID)
}
