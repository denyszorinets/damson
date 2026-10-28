package controller

import (
	"slices"
	"sync"
)

// registry maps agent ids to their live session. It is a socket cache, not
// state: the database decides who owns an agent.
type registry struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func newRegistry() *registry { return &registry{sessions: make(map[string]*session)} }

// replace installs s and returns the session it displaced, if any.
func (r *registry) replace(s *session) (old *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old = r.sessions[s.agentID]
	r.sessions[s.agentID] = s
	return old
}

// remove deletes s only if it is still the registered session for its agent.
func (r *registry) remove(s *session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[s.agentID] != s {
		return false
	}
	delete(r.sessions, s.agentID)
	return true
}

func (r *registry) get(agentID string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[agentID]
}

func (r *registry) all() []*session {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	return out
}

func (r *registry) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
