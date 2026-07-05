package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// sessionMeta holds persisted metadata for a chat session.
// Messages are stored separately via openagent.Memory.
type sessionMeta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// sessionStore persists session metadata to a JSON file.
// It is safe for concurrent use.
type sessionStore struct {
	mu       sync.Mutex
	path     string
	sessions map[string]*sessionMeta
}

func newSessionStore(path string) (*sessionStore, error) {
	s := &sessionStore{path: path, sessions: make(map[string]*sessionMeta)}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *sessionStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var wrapper struct {
		Sessions map[string]*sessionMeta `json:"sessions"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	if wrapper.Sessions != nil {
		s.sessions = wrapper.Sessions
	}
	return nil
}

func (s *sessionStore) save() error {
	wrapper := struct {
		Sessions map[string]*sessionMeta `json:"sessions"`
	}{Sessions: s.sessions}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func (s *sessionStore) Create(id string, title string) *sessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	meta := &sessionMeta{ID: id, Title: title, CreatedAt: now, UpdatedAt: now}
	s.sessions[id] = meta
	s.save()
	return meta
}

func (s *sessionStore) Get(id string) *sessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *sessionStore) List() []*sessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := make([]*sessionMeta, 0, len(s.sessions))
	for _, m := range s.sessions {
		list = append(list, m)
	}
	return list
}

func (s *sessionStore) Update(id string, title string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.sessions[id]
	if !ok {
		return false
	}
	m.Title = title
	m.UpdatedAt = time.Now()
	s.save()
	return true
}

func (s *sessionStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return false
	}
	delete(s.sessions, id)
	s.save()
	return true
}
