package util

import "sync"

// Gate prevents multiple concurrent pumpers for the same session.
type Gate struct {
	mu sync.Mutex
	in bool
}

func (g *Gate) TryEnter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.in {
		return false
	}
	g.in = true
	return true
}

func (g *Gate) Leave() {
	g.mu.Lock()
	g.in = false
	g.mu.Unlock()
}

var (
	gatesMu sync.Mutex
	gates   = map[string]*Gate{}
)

func GetGate(key string) *Gate {
	gatesMu.Lock()
	defer gatesMu.Unlock()
	if g, ok := gates[key]; ok {
		return g
	}
	g := &Gate{}
	gates[key] = g
	return g
}
