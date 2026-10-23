package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	openagent "github.com/yusheng-g/openagent-go"
	"github.com/yusheng-g/openagent-go/acp"
	openacp "github.com/yusheng-g/openagent-go/acp/sdk"
	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/cmd/cli/config"
	"github.com/yusheng-g/openagent-go/kernel"
	"github.com/yusheng-g/openagent-go/session"
)

// storeReloadFn installs fn into the process-wide settingsReloadFn and
// restores the previous value on cleanup, so tests stay independent.
func storeReloadFn(t *testing.T, fn func(ctx context.Context) acp.ReloadResult) {
	t.Helper()
	old := settingsReloadFn.Load()
	settingsReloadFn.Store(&fn)
	t.Cleanup(func() { settingsReloadFn.Store(old) })
}

// runLoopWithChannel runs the reload loop over a manually-fed channel so
// tests control signal delivery precisely. Returns a done channel closed
// when the loop exits.
func runLoopWithChannel(sigCh chan os.Signal) (context.CancelFunc, chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunReloadSignalLoop(ctx, sigCh)
		close(done)
	}()
	return cancel, done
}

// TestReloadSignalLoop_CallsStoredReloadFn: one signal → one reload, with
// the ReloadResult surfaced by the stored function reaching reloadOnce
// without error.
func TestReloadSignalLoop_CallsStoredReloadFn(t *testing.T) {
	var calls atomic.Int32
	storeReloadFn(t, func(ctx context.Context) acp.ReloadResult {
		calls.Add(1)
		return acp.ReloadResult{Applied: []string{"log level: info→debug"}}
	})

	sigCh := make(chan os.Signal, 4)
	cancel, done := runLoopWithChannel(sigCh)
	defer cancel()

	sigCh <- syscall.SIGHUP

	waitFor(t, 5*time.Second, func() bool { return calls.Load() == 1 })

	cancel()
	<-done
	if got := calls.Load(); got != 1 {
		t.Fatalf("reload calls = %d, want 1", got)
	}
}

// TestReloadSignalLoop_NilFnIsNoOpAndLoopSurvives: with no reload function
// stored (no server running) a signal must not panic or kill the loop —
// the next signal after a function is stored still reloads.
func TestReloadSignalLoop_NilFnIsNoOpAndLoopSurvives(t *testing.T) {
	old := settingsReloadFn.Load()
	settingsReloadFn.Store(nil)
	var calls atomic.Int32
	t.Cleanup(func() {
		settingsReloadFn.Store(old)
		if calls.Load() == 0 {
			t.Error("reload fn was never called after nil no-op")
		}
	})

	sigCh := make(chan os.Signal, 4)
	cancel, done := runLoopWithChannel(sigCh)
	defer cancel()

	sigCh <- syscall.SIGHUP
	// Give the loop time to process the nil-path signal; it must survive.
	time.Sleep(100 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("loop exited after nil-fn signal")
	default:
	}

	// Now a server "starts": store the fn and signal again.
	fn := func(ctx context.Context) acp.ReloadResult {
		calls.Add(1)
		return acp.ReloadResult{}
	}
	settingsReloadFn.Store(&fn)
	sigCh <- syscall.SIGHUP
	waitFor(t, 5*time.Second, func() bool { return calls.Load() == 1 })

	cancel()
	<-done
}

// TestReloadSignalLoop_CoalescesBurstDuringReload: three signals arriving
// while a reload is in flight must produce exactly two reloads (the
// in-flight one plus one coalesced follow-up), not three — the drain
// collapses the queued burst.
func TestReloadSignalLoop_CoalescesBurstDuringReload(t *testing.T) {
	reloadStarted := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	storeReloadFn(t, func(ctx context.Context) acp.ReloadResult {
		if calls.Add(1) == 1 {
			close(reloadStarted)
			<-release // hold the first reload open
		}
		return acp.ReloadResult{}
	})

	sigCh := make(chan os.Signal, 8)
	cancel, done := runLoopWithChannel(sigCh)
	defer cancel()

	sigCh <- syscall.SIGHUP
	<-reloadStarted
	// Both extra signals are queued while the loop is provably inside
	// reloadOnce, so the drain that follows must swallow one of them.
	sigCh <- syscall.SIGHUP
	sigCh <- syscall.SIGHUP
	close(release)

	waitFor(t, 5*time.Second, func() bool { return calls.Load() == 2 })
	time.Sleep(100 * time.Millisecond) // a third reload would land here
	if got := calls.Load(); got != 2 {
		t.Fatalf("reload calls = %d, want 2 (burst not coalesced)", got)
	}

	cancel()
	<-done
}

// TestReloadSignalLoop_RealSignal: a real SIGHUP delivered to this process
// triggers a reload and does not terminate it (the pre-fix default action).
// A second signal after the first reload proves the loop keeps serving.
func TestReloadSignalLoop_RealSignal(t *testing.T) {
	called := make(chan struct{}, 2)
	storeReloadFn(t, func(ctx context.Context) acp.ReloadResult {
		called <- struct{}{}
		return acp.ReloadResult{Applied: []string{"test applied"}}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Install the handler synchronously before any kill — a signal sent
	// before signal.Notify runs would take the default action and kill
	// the test process (the same startup race main.go avoids).
	sigCh := NotifySIGHUP()
	done := make(chan struct{})
	go func() {
		RunReloadSignalLoop(ctx, sigCh)
		close(done)
	}()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	select {
	case <-called:
		// Reaching this line proves the process survived the signal.
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not trigger a reload within 5s")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send second SIGHUP: %v", err)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("loop stopped serving after the first reload")
	}

	cancel()
	<-done
}

// ── Mid-generation interruption guard ──

// slowStream yields chunks with a delay so a generation is observably "in
// flight", and closes started on the first Next call.
type slowStream struct {
	chunks  []openagent.StreamChunk
	delay   time.Duration
	started chan struct{}
	once    *sync.Once
	i       int
}

func (s *slowStream) Next() bool {
	s.once.Do(func() { close(s.started) })
	if s.i >= len(s.chunks) {
		return false
	}
	time.Sleep(s.delay)
	s.i++
	return true
}
func (s *slowStream) Current() openagent.StreamChunk { return s.chunks[s.i-1] }
func (s *slowStream) Err() error                     { return nil }
func (s *slowStream) Close() error                   { return nil }

// slowModel streams a fixed chunk sequence on every call. started closes
// once on the first chunk of the FIRST stream — the once is shared by
// pointer so later streams never re-close it.
type slowModel struct {
	chunks    []openagent.StreamChunk
	delay     time.Duration
	started   chan struct{}
	startOnce sync.Once
}

func (m *slowModel) ChatCompletion(ctx context.Context, req openagent.ChatCompletionRequest) (*openagent.ChatCompletionResponse, error) {
	return nil, errors.New("slowModel: use ChatCompletionStream")
}

func (m *slowModel) ChatCompletionStream(ctx context.Context, req openagent.ChatCompletionRequest) (openagent.StreamReader, error) {
	return &slowStream{chunks: m.chunks, delay: m.delay, started: m.started, once: &m.startOnce}, nil
}

func (m *slowModel) ContextWindow() int { return 128_000 }

// recordingSender records every agent message delta, in order.
type recordingSender struct {
	mu       sync.Mutex
	messages []string
}

func (s *recordingSender) SendAgentMessage(text string) error {
	s.mu.Lock()
	s.messages = append(s.messages, text)
	s.mu.Unlock()
	return nil
}
func (s *recordingSender) joined() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.messages, "")
}
func (s *recordingSender) SendAgentThought(string) error             { return nil }
func (s *recordingSender) SendPlanUpdate([]openacp.PlanEntry) error  { return nil }
func (s *recordingSender) SendToolCall(openacp.ToolCallUpdate) error { return nil }
func (s *recordingSender) SendToolCallWithMeta(openacp.ToolCallUpdate, map[string]any) error {
	return nil
}
func (s *recordingSender) SendAvailableCommands([]openacp.AvailableCommand) error { return nil }
func (s *recordingSender) SendAvailableSkills([]openacp.AvailableSkill) error     { return nil }
func (s *recordingSender) SendModeUpdate(openacp.SessionModeId) error             { return nil }
func (s *recordingSender) SendConfigOptionUpdate([]openacp.SessionConfigOption) error {
	return nil
}
func (s *recordingSender) SendUsageUpdate(int, int, *openacp.Cost) error { return nil }
func (s *recordingSender) SendSessionInfo(string, map[string]any) error  { return nil }
func (s *recordingSender) SendHistoryMessage(string, string, string) error {
	return nil
}
func (s *recordingSender) SendHistoryMessageWithMeta(string, string, string, map[string]any) error {
	return nil
}

// updateRecordingClient is a ClientRequester whose SendSessionUpdate records
// config_option_update notifications (the frontend-visible reload effect)
// and settings_reload_result notifications (the reload outcome broadcast).
type updateRecordingClient struct {
	mu               sync.Mutex
	updates          []string
	reloads          []settingsReloadNotif
	lastModelCurrent string // CurrentValue of the "model" option in the latest config_option_update
}

// settingsReloadNotif is one recorded settings_reload_result notification.
type settingsReloadNotif struct {
	sid        string
	success    bool
	applied    []string
	violations []string
	parseError string
}

func (c *updateRecordingClient) SendSessionUpdate(sid openacp.SessionId, update openacp.SessionUpdate) error {
	c.mu.Lock()
	if update.SessionUpdate == "config_option_update" {
		c.updates = append(c.updates, string(sid))
		for i := range update.ConfigOptions {
			if update.ConfigOptions[i].ID == "model" {
				if v, ok := update.ConfigOptions[i].CurrentValue.(string); ok {
					c.lastModelCurrent = v
				}
			}
		}
	}
	if update.SessionUpdate == "settings_reload_result" {
		n := settingsReloadNotif{sid: string(sid)}
		if v, ok := update.Meta["success"].(bool); ok {
			n.success = v
		}
		if v, ok := update.Meta["applied"].([]string); ok {
			n.applied = v
		}
		if v, ok := update.Meta["violations"].([]string); ok {
			n.violations = v
		}
		if v, ok := update.Meta["parse_error"].(string); ok {
			n.parseError = v
		}
		c.reloads = append(c.reloads, n)
	}
	c.mu.Unlock()
	return nil
}
func (c *updateRecordingClient) configUpdates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.updates)
}
func (c *updateRecordingClient) reloadNotifs() []settingsReloadNotif {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]settingsReloadNotif(nil), c.reloads...)
}
func (c *updateRecordingClient) modelCurrent() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastModelCurrent
}
func (c *updateRecordingClient) RequestPermission(ctx context.Context, req openacp.RequestPermissionRequest) (*openacp.RequestPermissionResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) ReadTextFile(context.Context, openacp.ReadTextFileRequest) (*openacp.ReadTextFileResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) WriteTextFile(context.Context, openacp.WriteTextFileRequest) (*openacp.WriteTextFileResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) CreateTerminal(context.Context, openacp.CreateTerminalRequest) (*openacp.CreateTerminalResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) TerminalOutput(context.Context, openacp.TerminalOutputRequest) (*openacp.TerminalOutputResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) WaitForTerminalExit(context.Context, openacp.WaitForTerminalExitRequest) (*openacp.WaitForTerminalExitResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) KillTerminal(context.Context, openacp.KillTerminalRequest) (*openacp.KillTerminalResponse, error) {
	return nil, nil
}
func (c *updateRecordingClient) ReleaseTerminal(context.Context, openacp.ReleaseTerminalRequest) (*openacp.ReleaseTerminalResponse, error) {
	return nil, nil
}

// memStore is an in-memory session.Store.
type memStore struct {
	mu       sync.Mutex
	sessions map[string]session.SessionInfo
}

func newMemStore() *memStore { return &memStore{sessions: map[string]session.SessionInfo{}} }

func (s *memStore) Save(ctx context.Context, info session.SessionInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[info.ID] = info
	return nil
}
func (s *memStore) Get(ctx context.Context, id string) (*session.SessionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.sessions[id]; ok {
		return &v, nil
	}
	return nil, nil
}
func (s *memStore) List(ctx context.Context) ([]session.SessionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]session.SessionInfo, 0, len(s.sessions))
	for _, v := range s.sessions {
		out = append(out, v)
	}
	return out, nil
}
func (s *memStore) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
	return nil
}
func (s *memStore) Close() error { return nil }

// TestSIGHUP_MidGenerationReloadKeepsPrompt is the end-to-end guard for the
// reload-during-chat concern: a real SIGHUP fires through the real signal
// loop while a prompt is streaming, and the prompt must finish with its
// full message, the reload must land (new provider registered, frontend
// notified), and the same session must serve a second prompt.
func TestSIGHUP_MidGenerationReloadKeepsPrompt(t *testing.T) {
	ctx := context.Background()

	// A slow model: 8 segments x 80ms ≈ 640ms of in-flight generation.
	var segments []string
	var chunks []openagent.StreamChunk
	for i := 0; i < 8; i++ {
		seg := fmt.Sprintf("seg%d", i)
		segments = append(segments, seg)
		chunks = append(chunks, openagent.StreamChunk{
			Choices: []openagent.StreamDelta{{Content: seg}},
		})
	}
	chunks = append(chunks, openagent.StreamChunk{
		Choices: []openagent.StreamDelta{{FinishReason: "stop"}},
	})
	mdl := &slowModel{chunks: chunks, delay: 80 * time.Millisecond, started: make(chan struct{})}

	cfg := agent.New("test",
		agent.WithModel(mdl),
		agent.WithSystemPrompts("test"),
		agent.WithMaxTurns(3),
	)
	srv := acp.NewAgentServer(cfg, kernel.Deps{}, newMemStore(), map[string]openagent.Model{"test/m": mdl})
	client := &updateRecordingClient{}
	srv.SetClientRequester(client)

	// Settings file declaring a provider the running server does not have:
	// the reload registers newprov/newmodel and broadcasts the change.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	settings := `{
		"log": {"level": "info"},
		"provider": {"newprov": {"api_key": "sk-test", "models": ["newmodel"]}}
	}`
	if err := os.WriteFile(cfgPath, []byte(settings), 0644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	// Wire the production reload path: signal loop → stored fn → sw.reload.
	sw := &settingsWatcher{cfgPath: cfgPath, prev: &config.Config{}, srv: srv}
	reloadFn := func(ctx context.Context) acp.ReloadResult {
		r := sw.reload(ctx)
		return acp.ReloadResult{Applied: r.Applied, Violations: r.Violations, ParseError: r.ParseError}
	}
	storeReloadFn(t, reloadFn)

	loopCtx, cancelLoop := context.WithCancel(ctx)
	defer cancelLoop()
	// Install the handler synchronously (see the real-signal test): the
	// kill below must never race signal.Notify.
	hupCh := NotifySIGHUP()
	go RunReloadSignalLoop(loopCtx, hupCh)

	// Session through the public API.
	resp, err := srv.OnNewSession(ctx, openacp.NewSessionRequest{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("OnNewSession: %v", err)
	}
	sid := resp.SessionID

	prompt := func(sender *recordingSender) chan error {
		errCh := make(chan error, 1)
		go func() {
			_, err := srv.OnPrompt(ctx, openacp.PromptRequest{
				SessionID: sid,
				Prompt:    []openacp.ContentBlock{{Type: "text", Text: "hello"}},
			}, sender)
			errCh <- err
		}()
		return errCh
	}

	sender := &recordingSender{}
	promptErr := prompt(sender)

	// Wait until the model is provably mid-generation, then fire the signal.
	select {
	case <-mdl.started:
	case <-time.After(10 * time.Second):
		t.Fatal("model stream never started")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	// The prompt must complete with its full message.
	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("OnPrompt interrupted by mid-generation SIGHUP: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("prompt did not complete after mid-generation SIGHUP")
	}
	joined := sender.joined()
	for _, seg := range segments {
		if !strings.Contains(joined, seg) {
			t.Errorf("assistant message incomplete after reload: %q missing %q", joined, seg)
		}
	}

	// The reload must have landed: new provider in the registry, frontend
	// notified via config_option_update.
	if !containsModel(srv, "newprov/newmodel") {
		t.Errorf("reload did not register newprov/newmodel (registry: %v)", srv.ModelIDs())
	}
	if client.configUpdates() == 0 {
		t.Error("no config_option_update reached the client after reload")
	}
	// The reload outcome broadcast must reach the client too, with the
	// successful outcome of this reload.
	notifs := client.reloadNotifs()
	if len(notifs) == 0 {
		t.Fatal("no settings_reload_result reached the client after reload")
	}
	n := notifs[len(notifs)-1]
	if n.sid != string(sid) {
		t.Errorf("settings_reload_result sid = %q, want %q", n.sid, sid)
	}
	if !n.success {
		t.Errorf("settings_reload_result success = false, want true (applied=%v violations=%v parse_error=%q)",
			n.applied, n.violations, n.parseError)
	}

	// The same session must keep serving.
	sender2 := &recordingSender{}
	promptErr2 := prompt(sender2)
	select {
	case err := <-promptErr2:
		if err != nil {
			t.Fatalf("second OnPrompt after reload: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("second prompt did not complete after reload")
	}
	if !strings.Contains(sender2.joined(), segments[0]) {
		t.Errorf("second prompt produced no output: %q", sender2.joined())
	}
}

// waitFor polls cond until true or the deadline passes, failing the test on
// timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}
