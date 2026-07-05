package openagent

import (
	"context"
	"sync"
	"testing"
)

// fakeModel is a minimal Model that returns a fixed response and never calls tools.
type fakeModel struct {
	resp *ChatCompletionResponse
}

func (m *fakeModel) ChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	return m.resp, nil
}

func (m *fakeModel) ChatCompletionStream(ctx context.Context, req ChatCompletionRequest) (StreamReader, error) {
	return nil, nil
}

func (m *fakeModel) ContextWindow() int { return 128_000 }

func fakeNoopModel() Model {
	return &fakeModel{
		resp: &ChatCompletionResponse{
			Choices: []Choice{{
				Message: Message{
					Role:    RoleAssistant,
					Content: "Done.",
				},
				FinishReason: "stop",
			}},
			Usage: Usage{},
		},
	}
}

func TestAddAgent(t *testing.T) {
	t.Run("add to empty team", func(t *testing.T) {
		team := NewTeam()
		if err := team.AddAgent("a", "agent A", NewAgent("a", WithModel(fakeNoopModel()))); err != nil {
			t.Fatalf("AddAgent: %v", err)
		}
		if _, ok := team.agents["a"]; !ok {
			t.Fatal("agent not in map")
		}
		if len(team.order) != 1 || team.order[0] != "a" {
			t.Fatalf("order: %v", team.order)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		team := NewTeam()
		if err := team.AddAgent("x", "first", NewAgent("x", WithModel(fakeNoopModel()))); err != nil {
			t.Fatalf("first AddAgent: %v", err)
		}
		if err := team.AddAgent("x", "second", NewAgent("x", WithModel(fakeNoopModel()))); err == nil {
			t.Fatal("expected error on duplicate name")
		}
	})

	t.Run("multiple agents", func(t *testing.T) {
		team := NewTeam()
		if err := team.AddAgent("a", "A", NewAgent("a", WithModel(fakeNoopModel()))); err != nil {
			t.Fatalf("AddAgent a: %v", err)
		}
		if err := team.AddAgent("b", "B", NewAgent("b", WithModel(fakeNoopModel()))); err != nil {
			t.Fatalf("AddAgent b: %v", err)
		}
		if len(team.agents) != 2 {
			t.Fatalf("expected 2 agents, got %d", len(team.agents))
		}
	})
}

func TestRemoveAgent(t *testing.T) {
	t.Run("remove existing", func(t *testing.T) {
		team := NewTeam()
		team.AddAgent("a", "A", NewAgent("a", WithModel(fakeNoopModel())))
		team.AddAgent("b", "B", NewAgent("b", WithModel(fakeNoopModel())))

		team.RemoveAgent("a")
		if _, ok := team.agents["a"]; ok {
			t.Fatal("agent a still in map")
		}
		if len(team.order) != 1 || team.order[0] != "b" {
			t.Fatalf("order: %v", team.order)
		}
	})

	t.Run("remove non-existing", func(t *testing.T) {
		team := NewTeam()
		team.AddAgent("a", "A", NewAgent("a", WithModel(fakeNoopModel())))
		team.RemoveAgent("nonexistent") // no-op, no panic
		if len(team.agents) != 1 {
			t.Fatal("agent count changed")
		}
	})

	t.Run("remove last agent", func(t *testing.T) {
		team := NewTeam()
		team.AddAgent("a", "A", NewAgent("a", WithModel(fakeNoopModel())))
		team.RemoveAgent("a")
		if len(team.agents) != 0 {
			t.Fatal("team not empty")
		}
		if len(team.order) != 0 {
			t.Fatal("order not empty")
		}
	})
}

func TestConcurrentAddRemove(t *testing.T) {
	// Verify AddAgent/RemoveAgent don't race with reads.
	team := NewTeam()
	team.AddAgent("a", "A", NewAgent("a", WithModel(fakeNoopModel())))

	var wg sync.WaitGroup

	// Writer goroutine: add and remove agents
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			name := string(rune('b' + i%5))
			team.AddAgent(name, "dynamic", NewAgent(name, WithModel(fakeNoopModel())))
			team.RemoveAgent(name)
		}
	}()

	// Reader goroutine: simulate agentInfos / prepareAgent pattern
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			team.mu.Lock()
			_ = len(team.agents)
			for _, name := range team.order {
				_ = team.agents[name]
			}
			team.mu.Unlock()
		}
	}()

	wg.Wait()
}
