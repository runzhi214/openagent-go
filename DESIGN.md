# openagent-go Architecture

## Overview

openagent-go is a **fully pluggable**, open-source AI agent framework in Go. The core is a minimal mainline loop—all capabilities are added through pluggable modules.

**Design principles:**

- Follow industry standards (OpenAI API shape); no custom protocols
- The Runner is the sole mediator — modules never call each other
- No module configured = capability absent; nil means skip that node
- Avoid code bloat: think first, build second, no speculative abstractions
- Library code never reads environment variables (that's the application layer)

**Two extension paths:**

| Path | For | Mechanism |
|------|-----|-----------|
| Compile-time | Platform developers | Implement Go interface → inject via `WithXxx()` |
| Runtime | Community / end users | Drop `.wasm` files into plugin dir → auto-loaded |

Both coexist. Compile-time interfaces are the "backbone"; runtime plugins are one implementation source for those interfaces.

---

## 8-Node Mainline Loop

```
Agent.Run(ctx, session, input)
  │
  ├─ turn 1 only:
  │   ① Memory.Recent()     ← restore history + auto-compaction (Summarizer)
  │   ① Memory.Search()     ← semantic / keyword retrieval
  │   ③ Guard.in.Check()    ← input safety check
  │
  └─ for turn in 1..maxTurns:
      ② PromptBuilder() or defaultBuildPrompt()
         ├─ system instructions
         ├─ compressed summary + hints
         ├─ relevant facts (from Search)
         ├─ skill catalog + loaded skills
         └─ working messages
      ④ Model.ChatCompletionStream()  → fallback ChatCompletion()
         ├─ 429/503 → RetryableError → exponential backoff (max 3)
         └─ StreamTextDelta real-time push
      ⑤ Guard.out.Check()
      ⑥ Approver.Approve() → Tool.Execute() (concurrent goroutines)
         └─ tool result → Guard.out re-check
      ⑧ Memory.Append()
      has tool_calls → loop back to ②
      no tool_calls → StreamDone → return
```

Each node: `if module != nil { module.Call(...) }`.

---

## Core Types

### Agent

```go
type Agent struct {
    Name, Description, Instructions string
    Model       Model
    Tools       []Tool
    Memory      Memory
    Prompt      PromptBuilder    // nil = default
    InGuard     InputGuard
    OutGuard    OutputGuard
    Approver    Approver
    Hooks       RunHooks
    Observer    RunObserver      // nil = no stage events
    SkillLoader SkillLoader
    MaxTurns    int             // default 20
    WorkingMemN int             // default 10
}

agent.Run(ctx, session, input) → (*RunResult, error)
agent.RunStream(ctx, session, input) → <-chan StreamEvent
agent.RunGoal(ctx, session, goal) → (*RunResult, error)
agent.RunGoalStream(ctx, session, goal) → <-chan StreamEvent
agent.Clone() → *Agent
```

The Runner is private — `Agent.Run()` creates it internally.

### StreamEvent

```go
const (
    StreamTextDelta    = "text_delta"     // per-character output
    StreamToolCall     = "tool_call"      // tool invocation start
    StreamToolProgress = "tool_progress"  // streaming tool output chunk
    StreamToolResult   = "tool_result"    // tool result (final)
    StreamRetrying     = "retrying"       // 429 backoff in progress
    StreamDone         = "done"           // normal completion
    StreamError        = "error"          // execution failure
    StreamAborted      = "aborted"        // external interrupt (cancel/timeout)
)
```

### Session

```go
type Session struct {
    ID, UserID, AgentName, ModelID string
    Temperature, MaxTokens         float64 / int
    UserProfile, ProjectContext    string
    Turn                           int
    CreatedAt                      time.Time
}
```

Pure data carrier. The application layer owns CRUD. The Runner does not create Sessions.

---

## Module Interfaces

### ① Memory (Three-Layer Model)

```
Layer 1: Working    — Recent() returns last N messages; triggers auto-compaction
Layer 2: Compressed — Compressed() returns summary + hints; auto-updated by Layer 1
Layer 3: Archive    — Search() full-text / vector; Append() writes
```

```go
type Memory interface {
    io.Closer
    Recent(ctx, sessionID, n int) ([]Message, error)
    Compressed(ctx, sessionID) (*CompressedContext, error)
    Search(ctx, sessionID, query string, limit int) ([]SearchResult, error)
    Append(ctx, sessionID, msg Message) error
    DeleteSession(ctx, sessionID) error
}
```

The Runner does not know about compaction. `Recent()` internally: message count > workingN + Summarizer configured → call `Summarizer.Summarize()` → store `CompressedContext` → delete old messages → return trimmed result.

Implementations: `memory/file` (JSONL, zero-dependency), `memory/sqlite` (SQLite + FTS5, optional vector search via `WithEmbedder`).

### Summarizer (Memory dependency)

```go
type Summarizer interface {
    Summarize(ctx context.Context, messages []Message) (*CompressedContext, error)
}
```

nil = no compaction. Configured on Memory via `WithSummarizer()`.

### Embedder (Memory dependency)

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float64, error)
    Dimensions() int
}
```

nil = fallback to keyword/FTS5 search. Configured on Memory via `WithEmbedder()`.

### ② PromptBuilder

```go
type PromptBuilder func(ctx context.Context, input PromptInput) ([]Message, error)

type PromptInput struct {
    AgentName, AgentDescription, Instructions string
    WorkingMessages   []Message
    Compressed        *CompressedContext
    RelevantFacts     []string
    Tools             []FunctionDefinition
    AvailableSkills   []SkillInfo
    LoadedSkills      map[string]string
    UserProfile, ProjectContext string
}
```

Function type — single method, no state needed. nil = `defaultBuildPrompt()`.

### ③ / ⑤ Guard

```go
type InputGuard interface {
    Check(ctx, input GuardInput) GuardResult
}
type OutputGuard interface {
    Check(ctx, output GuardOutput) GuardResult
}

type GuardResult struct {
    Allowed  bool
    Reason   string
    Tripwire bool  // true → terminate the run
}
```

InGuard checks once before the loop. OutGuard checks every model output + every tool result. Implementation: `guard/llm` — LLM-as-judge for content safety.

### ④ Model

```go
type Model interface {
    ChatCompletion(ctx, req) (*ChatCompletionResponse, error)
    ChatCompletionStream(ctx, req) (StreamReader, error)  // nil,nil = not supported
    ContextWindow() int
}
```

Implementation: `model/openai` (openai-go v3 SDK). Streaming preferred, non-streaming fallback.

### Tool

```go
type Tool interface {
    Definition() FunctionDefinition
    Execute(ctx context.Context, args json.RawMessage) (string, error)
}

type StreamExecutor interface {
    ExecuteStream(ctx context.Context, args json.RawMessage) <-chan ToolStreamChunk
}
```

Built-in tools: `shell`, `read`, `write`, `ls`, `grep` (in `tool/` package). Skill tools (`use_skill`, `reload_skills`) auto-injected by the Runner. WASM tool plugins via `plugin/wasm`.

### ⑥ Approver

```go
type Approver interface {
    Approve(ctx, call ToolCall, def FunctionDefinition, session Session) (allowed bool, reason string)
}
```

nil = allow all. Implementations: `cmd/tui` (bubbletea v2 Y/N), `examples/webui` (SSE dialog with Allow Once / Allow Directory).

### ⑦ RunHooks

```go
type RunHooks interface {
    OnAgentStart(ctx, req) error
    OnAgentEnd(ctx, req, resp, err)
    OnToolStart(ctx, tool, args) error
    OnToolEnd(ctx, tool, args, result, err)
}
```

Aligned with OpenAI Agents SDK naming. Implementations: `hooks/slog`, `hooks/otel`.

### RunObserver

```go
type RunObserver interface {
    ObserveStage(ctx context.Context, event StageEvent)
}

type StageEvent struct {
    Name     string         // "memory.fetch", "model.call", ...
    Phase    string         // "enter" or "leave"
    Detail   map[string]any // turn, tokens, tool name, ...
    Duration time.Duration  // wall-clock on "leave"
    Err      error
}
```

Per-stage enter/leave events with durations. Use for pipeline panels, tracing, monitoring. Multiple observers via `MultiObserver()`.

### Skill

```go
type SkillLoader interface {
    Discover(ctx) ([]SkillInfo, error)
    Load(ctx, skill SkillInfo) (string, error)
}
```

Workflow: Discover → inject catalog into prompt → model calls `use_skill(name)` → Load returns full body. `reload_skills` rescans and prunes removed skills. Implementation: `skill/fs`.

---

## Module Non-Interference

The Runner is the sole mediator. Modules never call each other:

```
Runner.buildPrompt:
  msgs = Memory.Recent()      ← Memory may call Summarizer internally
  cc   = Memory.Compressed()
  facts = Memory.Search()      ← Memory may call Embedder internally
  input = PromptInput{...}
  result = PromptBuilder(input)

Runner ferries data:
  - Memory → PromptInput
  - Tool.Execute result → Memory.Append
  - Model response → Guard → Approver → Tool
```

**Key:** `Embedder` and `Summarizer` are Memory dependencies, not Agent dependencies. They are configured on Memory at construction time.

---

## Directory Structure

```
openagent-go/
├── agent.go              Agent + Run/RunStream/RunGoal/Clone + StreamEvent
├── runner.go             private runner + 8-node loop + defaultBuildPrompt
├── model.go              Model, Embedder, Summarizer interfaces + request/response types
├── message.go            Message + ContentPart (multimodal)
├── tool.go               Tool interface + FunctionDefinition + StreamExecutor
├── sandbox.go            Sandbox interface + Command/Result types
├── memory.go             Memory interface + CompressedContext
├── prompt.go             PromptInput + PromptBuilder + RetrievalHint
├── guard.go              InputGuard / OutputGuard
├── approver.go           Approver
├── hooks.go              RunHooks
├── observer.go           RunObserver + StageEvent
├── skill.go              SkillLoader + SkillInfo
├── router.go             Router + FirstAgentRouter + LLMRouter
├── team.go               Team + TeamResult + HandoffEntry + handoffTool
├── options.go            WithXxx() AgentOption + TeamOption
├── session.go            Session
├── doc.go                Package documentation
│
├── tool/                 Built-in Tool implementations
│   ├── shell.go          Shell: OS sandbox command execution with streaming
│   ├── file.go           ReadFile / WriteFile / ListDir (path traversal protection)
│   └── grep.go           Grep: recursive file search
│
├── sandbox/native/       OS-native sandbox
│   ├── native.go         New() factory + public API
│   ├── native_darwin.go  macOS: sandbox-exec + Seatbelt profile
│   ├── native_linux.go   Linux: bwrap namespace isolation
│   └── native_windows.go Windows: stub
│
├── model/openai/         OpenAI model implementation
├── memory/file/          JSONL file memory
├── memory/sqlite/        SQLite + FTS5 + vector search
├── guard/llm/            LLM-as-judge guard
├── hooks/slog/           slog logger hooks
├── hooks/otel/           OpenTelemetry tracing hooks
├── skill/fs/             Filesystem skill loader
├── plugin/wasm/          WASM plugin runtime (wazero)
├── acp/                  ACP protocol (Agent ↔ IDE)
├── mcp/                  MCP protocol (tool interoperability)
├── plan/                 Goal → DAG → parallel execution + replan
├── eventbus/             Generic pub/sub with history replay
├── rest/                 REST API (HTTP access layer)
├── runner/acp/           ACP Runner: external agent as Team member
│
├── cmd/
│   ├── tui/              Terminal chat (bubbletea v2)
│   └── cli/              CLI tool (run + goal commands)
│
├── examples/
│   ├── basic/            Non-streaming example
│   ├── stream/           Streaming example
│   ├── skill/            Skill loading example
│   ├── memory/           Memory persistence example
│   ├── hooks/            Lifecycle hooks example
│   ├── observer/         Stage observer example
│   ├── guard/            Safety guard example
│   ├── team/             Multi-agent team example
│   ├── delegate/         Agent-as-tool parallel delegation
│   ├── plugin/           WASM plugin example
│   ├── sandbox/          Sandbox demo
│   └── webui/            Web UI demo (SSE + approval + plan + goal + pipeline)
│
├── DESIGN.md             Architecture (English)
├── DESIGN.zh.md          Architecture (Chinese)
└── README.md
```

All interfaces in root package. Implementations in sub-packages. No circular dependencies.

---

## Key Design Decisions

**1. Why is Runner private?** Users call `Agent.Run()`, never construct a Runner. Runner is an internal implementation detail.

**2. Why is compaction inside Memory?** The Runner should not know storage strategy. Memory manages when to compact and how to store summaries. Clean module boundary.

**3. Why aren't Embedder/Summarizer on Agent?** They are Memory dependencies, not Agent capabilities. Memory decides whether it needs embeddings or summaries. Preserves "modules don't call each other".

**4. Why streaming by default?** `callModelOnce` prefers `ChatCompletionStream`, falls back to non-streaming. Lowest time-to-first-token.

**5. Why is PromptBuilder a function type?** One method, no state. Function types are simpler.

**6. Why is Handoff a Tool rather than Router choosing each step?** The model has full context and makes better handoff decisions than a router. Router only does two things: initial message routing and policy vetoes.

**7. Why inject hints instead of erroring on loops?** Two-layer loop detection: first give the model a hint ("you're in a loop, answer directly"), then remove transfer_to_* tools if it persists. Graceful degradation.

**8. Why independent Memory per Agent instead of shared Team Memory?** Keep it simple. Agent already supports independent Memory. Add shared memory later if needed, without breaking existing interfaces.

---

## Extension Paths

### Compile-time Extensions (Go interfaces)

| Node | Interface | Status | Notes |
|------|-----------|--------|-------|
| ①② | Memory | ✅ | file / sqlite |
| ① | Embedder | ✅ | nil = keyword fallback |
| ① | Summarizer | ✅ | nil = no compaction |
| ② | PromptBuilder | ✅ | function type, nil = default |
| ④ | Model | ✅ | OpenAI implementation |
| ⑥ | Tool | ✅ | compile-time + builtin + WASM |
| — | SkillLoader | ✅ | filesystem implementation |
| ③ | InputGuard | ✅ | guard/llm |
| ⑤ | OutputGuard | ✅ | guard/llm |
| ⑥ | Approver | ✅ | TUI + WebUI, human-in-the-loop |
| ⑦ | RunHooks | ✅ | slog + OpenTelemetry |
| — | RunObserver | ✅ | per-stage enter/leave, Runner wired |
| — | Router | ✅ | first-agent + LLM-based |
| — | Team | ✅ | multi-agent with handoff + loop detection |
| — | Plan | ✅ | goal → DAG → parallel execution + replan |
| — | EventBus | ✅ | generic pub/sub, per-session topics |

### Runtime Extensions (WASM Plugins)

```go
// No plugins: zero overhead
agent := openagent.NewAgent("bot", openagent.WithModel(model))

// With plugins:
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

mgr := wasm.NewManager("./plugins")
mgr.Discover(ctx)
mgr.OnAbort(func(reason string) { cancel() })

agent := openagent.NewAgent("bot",
    openagent.WithModel(model),
    openagent.WithTools(mgr.Tools()...),
    openagent.WithRunObserver(mgr.Observer()),
)
```

**Plugin types:**

| Type | Purpose | Injected as | ABI exports |
|------|---------|------------|-------------|
| Tool | New tools | `openagent.Tool` | `alloc`, `metadata`, `execute` |
| Stage | Stage event observation + abort | `RunObserver` | `alloc`, `metadata`, `run` |

WASM runtime: [wazero](https://github.com/tetratelabs/wazero) — pure Go, zero CGO. One `.wasm` file per plugin.

---

## Team (Multi-Agent Orchestration)

Team is an orchestration layer above the single-agent loop. Handoff = Tool with `EndTurn: true`. Each agent has independent Memory, Tools, and Guard.

```go
team := openagent.NewTeam(
    openagent.WithTeamAgent("researcher", "analyzes questions", researcher),
    openagent.WithTeamAgent("calculator", "performs math", calculator),
)
result, _ := team.Run(ctx, session, input)
stream := team.RunStream(ctx, session, input)
```

**Loop detection (layered):**

| Layer | Detection | Action |
|-------|-----------|--------|
| L1 Ping-pong | A→B→A→B pattern | Inject hint |
| L2 Frequency | Same agent ≥3 times | Inject hint |
| L3 Hard limit | Hint followed by another handoff | Remove transfer_to_* tools |

No hard error — graceful degradation.

### Router

```go
type AgentInfo struct {
    Name        string
    Description string
    Type        AgentType // AgentInternal or AgentExternal
}

type Router interface {
    Route(ctx, input, agents) (string, error)
    CanHandoff(ctx, entry, chain, session) error
}
```

`AgentInfo.Type` auto-populated: `WithTeamAgent` → `AgentInternal`, `AddAgent` with ACP runner → `AgentExternal`. Flows to Router, Team prompt, Plan planner, and WebUI.

### Agent as Tool (Parallel Delegation)

```go
coordinator := openagent.NewAgent("coordinator",
    openagent.WithTools(researcher.AsTool(), writer.AsTool()),
)
```

Three-layer isolation: new session per call, no coordinator history leaked, only the task string as input.

---

## Plan (DAG Goal Decomposition)

```go
p := plan.NewPlan(
    plan.WithPlanner(plan.NewLLMPlanner(model)),
    plan.WithAgent("coder", "writes code", coderAgent),
    plan.WithAgent("reviewer", "reviews code", reviewerAgent),
)
result, _ := p.Run(ctx, session, "Build a REST API for todos")
```

| | Team | Plan |
|---|------|------|
| Decision | Runtime, agent-initiated handoff | Pre-execution, Planner generates DAG |
| Parallelism | None (serial handoff chain) | Topological batches auto-parallel |
| Failure | Agent handles itself | Subtree replan with LLM |
| Use case | Conversational collaboration | Structured execution pipelines |

`Planner` generates a DAG from the goal. `Executor` topo-sorts into batches, runs each batch with goroutines, auto-replans on failure (up to `MaxReplans` times). `AutoReplan=false` pauses on failure for manual retry/replan.

## Goal (Autonomous Mode)

```go
agent.RunGoal(ctx, session, "Fix all failing tests")
```

Unlike `Run()` where the input is a user message that may scroll out of context, `RunGoal()` injects the goal into the system prompt — it persists across all turns. The agent iterates autonomously: plan → execute → evaluate → continue until done or impossible.

| | Run | RunGoal | Plan.Run |
|---|---|---|---|
| Goal placement | User message (scrolls out) | System prompt (persistent) | Planner-generated DAG |
| Behavior | One-shot Q&A | Autonomous iteration | DAG parallel execution |
| Stops when | No more tool calls | Goal achieved or impossible | All steps complete |

## EventBus

```go
bus := eventbus.New[MyEvent](500) // max 500 history events per session
sub := bus.Subscribe(sessionID)   // subscribe (auto-replays history)
bus.Publish(sessionID, evt)       // fanout to all subscribers
```

Generic pub/sub with per-session topics and history replay. Used by WebUI for multi-tab sync, plan progress, and pipeline panel events.

## Sandbox

OS-native security: macOS Seatbelt (`sandbox-exec`), Linux Bubblewrap (`bwrap`), Windows stub.

```go
sb, _ := native.New("./workspace")
// File tools: direct host I/O with path traversal protection
// Shell tool: fork → OS sandbox → exec
```

Three-layer security: file tools validate paths → shell tool runs inside OS sandbox → workspace boundary enforced at both levels.

---

## Pipeline Panel (Observability)

The Runner emits `StageEvent` at each of the 8 nodes (enter/leave). A `RunObserver` implementation bridges these to the frontend, where an SVG flow diagram renders live:

- 7 nodes: Fetch → Guard-In → Prompt → Model → Guard-Out → Tool → Store
- Status: gray (pending) → blue pulse (active) → green (done + duration) → red (error)
- Animated particles flow along connectors when data moves between stages
- Info bar: round counter + token usage

The observer adds `turn`/`maxTurns` and `tokens_prompt`/`tokens_completion` to `model.call` detail so the frontend shows per-round progress.

---

## Comparison

| | openai-agents | Claude Code | openagent-go |
|---|---|---|---|
| Sandbox | Docker SDK + macOS sandbox-exec | seccomp + namespaces | macOS Seatbelt / Linux bwrap |
| File tools | read/write/ls/rm/mkdir | Read/Write/Glob | ReadFile/WriteFile/ListDir/Grep |
| Streaming | PTY-based | Bash tool | Shell tool (line streaming, no PTY) |
| Multi-agent | Handoff chain | — | Team (handoff) + Plan (DAG parallel) |
| Goal mode | — | `/goal` | RunGoal + Plan.Run |
| Observability | — | — | RunObserver + SVG pipeline panel |
| Plugins | — | — | WASM (wazero, zero CGO) |

---

## UI Examples

- `cmd/cli/` — CLI tool: `openagent run "msg"` and `openagent goal "task"`, streaming output
- `cmd/tui/` — bubbletea v2 terminal chat, streaming + Y/N approval
- `examples/webui/` — browser chat with SSE streaming, plan DAG rendering, pipeline panel, permission memory, and `/plan` + `/goal` slash commands
