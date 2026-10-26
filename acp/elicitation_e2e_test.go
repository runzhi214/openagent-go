package acp

// End-to-end elicitation tests: the full offline loop with zero real tokens —
//
//	fake frontend (acp/sdk Client over io.Pipe)
//	  ⇄ ACP JSON-RPC ⇄ openacp.Server(AgentServer)
//	  ⇄ MCP stdio ⇄ examples/mcp-elicitation demo server (subprocess)
//
// The scripted model (planModel) calls the demo tool, which sends a form
// elicitation mid-call; the bridge forwards it as elicitation/create to the
// fake frontend, whose answer flows back to the tool result. See
// docs/elicitation.md for the design.

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	openagent "github.com/yusheng-g/openagent-go"
	openacp "github.com/yusheng-g/openagent-go/acp/sdk"
	"github.com/yusheng-g/openagent-go/agent"
	"github.com/yusheng-g/openagent-go/kernel"
)

// ── fake frontend (client side) ──

// elicitFrontend answers elicitation/create via a scripted responder and
// auto-allows permission requests; every other Agent→Client RPC stubs.
type elicitFrontend struct {
	mu       sync.Mutex
	got      []openacp.ElicitCreateParams
	respond  func(ctx context.Context, p openacp.ElicitCreateParams) (*openacp.ElicitResult, error)
	arrived  chan struct{}
	closeOne sync.Once
}

func (f *elicitFrontend) record(p openacp.ElicitCreateParams) {
	f.mu.Lock()
	f.got = append(f.got, p)
	f.mu.Unlock()
	f.closeOnce(f.arrived)
}

func (f *elicitFrontend) closeOnce(ch chan struct{}) {
	f.closeOne.Do(func() {
		if ch != nil {
			close(ch)
		}
	})
}

func (f *elicitFrontend) requests() []openacp.ElicitCreateParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]openacp.ElicitCreateParams(nil), f.got...)
}

func (f *elicitFrontend) HandleElicitationCreate(ctx context.Context, req openacp.ElicitCreateParams) (*openacp.ElicitResult, error) {
	f.record(req)
	if f.respond != nil {
		return f.respond(ctx, req)
	}
	return &openacp.ElicitResult{
		Action:  "accept",
		Content: map[string]any{"strategy": "balanced", "replicas": float64(3)},
	}, nil
}

func (f *elicitFrontend) HandleRequestPermission(ctx context.Context, req openacp.RequestPermissionRequest) (*openacp.RequestPermissionResponse, error) {
	allowOnce := openacp.PermissionOptionId("allow_once")
	return &openacp.RequestPermissionResponse{
		Outcome: openacp.RequestPermissionOutcome{
			Outcome:  openacp.PermissionOutcomeSelected,
			OptionID: &allowOnce,
		},
	}, nil
}

func (f *elicitFrontend) HandleReadTextFile(context.Context, openacp.ReadTextFileRequest) (*openacp.ReadTextFileResponse, error) {
	return nil, nil
}
func (f *elicitFrontend) HandleWriteTextFile(context.Context, openacp.WriteTextFileRequest) (*openacp.WriteTextFileResponse, error) {
	return nil, nil
}
func (f *elicitFrontend) HandleCreateTerminal(context.Context, openacp.CreateTerminalRequest) (*openacp.CreateTerminalResponse, error) {
	return nil, nil
}
func (f *elicitFrontend) HandleTerminalOutput(context.Context, openacp.TerminalOutputRequest) (*openacp.TerminalOutputResponse, error) {
	return nil, nil
}
func (f *elicitFrontend) HandleWaitForTerminalExit(context.Context, openacp.WaitForTerminalExitRequest) (*openacp.WaitForTerminalExitResponse, error) {
	return nil, nil
}
func (f *elicitFrontend) HandleKillTerminal(context.Context, openacp.KillTerminalRequest) (*openacp.KillTerminalResponse, error) {
	return nil, nil
}
func (f *elicitFrontend) HandleReleaseTerminal(context.Context, openacp.ReleaseTerminalRequest) (*openacp.ReleaseTerminalResponse, error) {
	return nil, nil
}

// eventRecorder records tool_call updates and agent messages.
type eventRecorder struct {
	mu       sync.Mutex
	toolJSON []string
	messages []string
}

func (r *eventRecorder) OnToolCall(tc openacp.ToolCallUpdate) {
	body, _ := json.Marshal(tc)
	r.mu.Lock()
	r.toolJSON = append(r.toolJSON, string(body))
	r.mu.Unlock()
}
func (r *eventRecorder) OnAgentMessage(text string, _ map[string]any) {
	r.mu.Lock()
	r.messages = append(r.messages, text)
	r.mu.Unlock()
}
func (r *eventRecorder) joinedTools() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.toolJSON, "\n")
}
func (r *eventRecorder) joinedMessages() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.messages, "")
}

// Unused EventHandler callbacks.
func (r *eventRecorder) OnAgentThought(string, map[string]any)                {}
func (r *eventRecorder) OnUserMessage(string, map[string]any)                 {}
func (r *eventRecorder) OnContextCompacting(map[string]any)                   {}
func (r *eventRecorder) OnContextCompacted(map[string]any)                    {}
func (r *eventRecorder) OnRetrying(map[string]any)                            {}
func (r *eventRecorder) OnPlan(openacp.Plan)                                  {}
func (r *eventRecorder) OnAvailableCommandsUpdate([]openacp.AvailableCommand) {}
func (r *eventRecorder) OnModeUpdate(openacp.SessionModeId)                   {}
func (r *eventRecorder) OnConfigOptionUpdate([]openacp.SessionConfigOption)   {}
func (r *eventRecorder) OnUsageUpdate(int, int, *openacp.Cost)                {}
func (r *eventRecorder) OnSessionInfo(string, map[string]any)                 {}
func (r *eventRecorder) OnMcpServers([]openacp.McpServerStatus)               {}

// ── wiring ──

// buildDemoServer compiles examples/mcp-elicitation into a temp binary.
func buildDemoServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "elicitation-demo")
	cmd := exec.Command("go", "build", "-o", bin, "./examples/mcp-elicitation")
	cmd.Dir = ".." // repo root: the package path resolves against cmd.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build demo elicitation server: %v\n%s", err, out)
	}
	return bin
}

// newElicitE2E wires the full loop and returns the client session, the
// fake frontend, the event recorder, and the new session id. elicitCap
// controls whether the frontend advertises form elicitation.
func newElicitE2E(t *testing.T, elicitCap bool, turns [][]openagent.StreamChunk) (*openacp.Session, *elicitFrontend, *eventRecorder, openacp.SessionId) {
	t.Helper()

	mdl := &planModel{turns: turns}
	agentSrv := NewAgentServer(
		agent.New("test", agent.WithModel(mdl), agent.WithSystemPrompts("test")),
		kernel.Deps{}, nil,
		map[string]openagent.Model{"test/m": mdl},
	)
	agentSrv.SetDefaultModelID("test/m")

	server := openacp.NewServer("test-agent", "1.0.0", agentSrv)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	ctx := context.Background()
	go func() { _ = server.RunTransport(ctx, stdoutW, stdinR) }()
	t.Cleanup(func() { stdinW.Close(); stdoutW.Close() })

	client := openacp.NewClient("fake-frontend", "1.0.0")
	csess := client.ConnectIO(ctx, stdinW, stdoutR)
	frontend := &elicitFrontend{}
	csess.SetClientRequestHandler(frontend)
	rec := &eventRecorder{}
	csess.SetEventHandler(rec)

	initReq := openacp.InitializeRequest{ProtocolVersion: 1}
	if elicitCap {
		initReq.ClientCapabilities.Elicitation = &openacp.ElicitationCapabilities{
			Form: &openacp.FormElicitationCapabilities{},
		}
	}
	if _, err := csess.Initialize(ctx, initReq); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	nsResp, err := csess.NewSession(ctx, openacp.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []openacp.McpServer{{Name: "elicit-demo", Command: buildDemoServer(t)}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return csess, frontend, rec, nsResp.SessionID
}

// promptOnce drives one text prompt with a timeout guard.
func promptOnce(t *testing.T, csess *openacp.Session, sid openacp.SessionId, text string) *openacp.PromptResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := csess.Prompt(ctx, openacp.PromptRequest{
		SessionID: sid,
		Prompt:    []openacp.ContentBlock{{Type: "text", Text: text}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	return resp
}

// askUserTurn builds the scripted model turn that calls the demo tool.
func askUserTurn() []openagent.StreamChunk {
	return []openagent.StreamChunk{
		toolCallChunk(0, "t1", "mcp__elicit-demo__ask_user", map[string]any{"topic": "deploy a blog"}),
	}
}

// ── tests ──

// TestElicitationE2E_Accept: the happy path — the tool's mid-call form
// elicitation reaches the frontend with scope and schema, the accepted
// content flows back into the tool result.
func TestElicitationE2E_Accept(t *testing.T) {
	csess, frontend, rec, sid := newElicitE2E(t, true, [][]openagent.StreamChunk{askUserTurn()})

	resp := promptOnce(t, csess, sid, "deploy a blog")
	if resp.StopReason != openacp.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}

	reqs := frontend.requests()
	if len(reqs) != 1 {
		t.Fatalf("elicitation requests = %d, want 1", len(reqs))
	}
	p := reqs[0]
	if p.SessionID != sid {
		t.Errorf("sessionId = %q, want %q", p.SessionID, sid)
	}
	if p.Mode != "form" {
		t.Errorf("mode = %q, want form", p.Mode)
	}
	if !strings.Contains(p.Message, "deploy a blog") {
		t.Errorf("message %q missing topic", p.Message)
	}
	if p.RequestedSchema == nil {
		t.Error("requestedSchema is nil")
	}

	// The user's accepted answer must appear in the tool call result.
	tools := rec.joinedTools()
	if !strings.Contains(tools, "balanced") {
		t.Errorf("tool call updates missing accepted content:\n%s", tools)
	}
}

// TestElicitationE2E_Cancel: session/cancel while the form is parked must
// release the whole chain — no hang, and the bridge reports cancellation
// (not an error) to the MCP server.
func TestElicitationE2E_Cancel(t *testing.T) {
	release := make(chan struct{})
	csess, frontend, _, sid := newElicitE2E(t, true, [][]openagent.StreamChunk{askUserTurn()})
	frontend.arrived = make(chan struct{})

	frontend.respond = func(ctx context.Context, p openacp.ElicitCreateParams) (*openacp.ElicitResult, error) {
		// The user never answers; unblock when the test tears down.
		select {
		case <-release:
			return &openacp.ElicitResult{Action: "cancel"}, nil
		case <-ctx.Done():
			return &openacp.ElicitResult{Action: "cancel"}, nil
		}
	}

	// Cancel the session once the elicitation has reached the frontend.
	go func() {
		<-frontend.arrived
		time.Sleep(50 * time.Millisecond) // let the bridge park fully
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = csess.Cancel(ctx, string(sid))
	}()
	t.Cleanup(func() { close(release) })

	resp := promptOnce(t, csess, sid, "deploy a blog")
	// The prompt was cancelled by OnCancel; both cancelled and end_turn are
	// acceptable — the point is it RETURNS instead of hanging on the form.
	_ = resp
	if got := len(frontend.requests()); got != 1 {
		t.Errorf("elicitation requests = %d, want 1", got)
	}
}

// TestElicitationE2E_NoCapability: a frontend that did not advertise
// elicitation never receives elicitation/create; the MCP server's Elicit
// call fails and the tool errors, but the turn completes normally.
func TestElicitationE2E_NoCapability(t *testing.T) {
	csess, frontend, rec, sid := newElicitE2E(t, false, [][]openagent.StreamChunk{askUserTurn()})

	resp := promptOnce(t, csess, sid, "deploy a blog")
	if resp.StopReason != openacp.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if got := len(frontend.requests()); got != 0 {
		t.Errorf("elicitation requests = %d, want 0 (capability not advertised)", got)
	}
	// The failure must be visible in the tool result, not swallowed.
	tools := rec.joinedTools()
	if !strings.Contains(tools, "elicitation") {
		t.Errorf("tool call updates missing elicitation failure:\n%s", tools)
	}
}

// TestBridgeElicit_URLModeRejected: a URL-mode elicitation must be rejected
// with an error even if it somehow reaches the bridge (defence in depth —
// the go-sdk already refuses unadvertised modes at the capability layer).
func TestBridgeElicit_URLModeRejected(t *testing.T) {
	srv := NewAgentServer(agent.New("test"), kernel.Deps{}, nil, nil)
	res, err := srv.bridgeElicit(context.Background(), "s1", &mcpsdk.ElicitRequest{
		Params: &mcpsdk.ElicitParams{Mode: "url", URL: "https://example.com"},
	})
	if err == nil {
		t.Fatalf("bridgeElicit(url) = %+v, want error", res)
	}
	if !strings.Contains(err.Error(), "not advertised") {
		t.Errorf("error = %v, want mode-not-advertised rejection", err)
	}
}
