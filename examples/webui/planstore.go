package main

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/yusheng-g/openagent-go/plan"
)

// persistedPlan holds a plan that can survive server restart.
type persistedPlan struct {
	Def   *plan.PlanDef   `json:"def"`
	State *plan.PlanState `json:"state"`
}

// planStore persists PlanDef + PlanState to a JSON file.
// Safe for concurrent use.
type planStore struct {
	mu    sync.Mutex
	path  string
	plans map[string]*persistedPlan // sessionID → plan
}

func newPlanStore(path string) (*planStore, error) {
	s := &planStore{path: path, plans: make(map[string]*persistedPlan)}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func (s *planStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var wrapper struct {
		Plans map[string]*persistedPlan `json:"plans"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return err
	}
	if wrapper.Plans != nil {
		s.plans = wrapper.Plans
	}
	return nil
}

func (s *planStore) save() error {
	wrapper := struct {
		Plans map[string]*persistedPlan `json:"plans"`
	}{Plans: s.plans}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// Save persists the plan for a session.
func (s *planStore) Save(sessionID string, def *plan.PlanDef, state *plan.PlanState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[sessionID] = &persistedPlan{Def: def, State: state}
	s.save()
}

// Load returns the persisted plan for a session, or nil.
func (s *planStore) Load(sessionID string) *persistedPlan {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.plans[sessionID]
}

// Delete removes a persisted plan.
func (s *planStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plans, sessionID)
	s.save()
}

// Has returns true if there is a persisted plan for the session that can be
// resumed (status is running or waiting_retry).
func (s *planStore) Has(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[sessionID]
	if !ok || p.State == nil {
		return false
	}
	return p.State.Status == plan.PlanStatusRunning
}
