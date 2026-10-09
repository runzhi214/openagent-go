package openviking

import (
	"context"
	"log/slog"
	"strings"

	openagent "github.com/yusheng-g/openagent-go"
	ctxpkg "github.com/yusheng-g/openagent-go/context"
)

// ConversationSyncer implements ctxpkg.Extractor in conversation-sync
// mode: each finished run's new messages (runDelta) are forwarded to the
// per-conversation OV session as a NATIVE user↔assistant exchange, and
// the server's VLM is the only extractor. This replaces the double
// distillation of the default mode (local LLM pass → fragment buffer →
// server VLM re-extraction) with the exchange structure OV's extraction
// contract is designed for: user-role content feeds the user-memory
// types (profile/preferences/entities/events), assistant-role content
// provides the dialogue context.
//
// The whole-transcript `messages` argument is ignored — runDelta is the
// incremental cursor, accumulated across runs by AsyncExtractor.
type ConversationSyncer struct {
	client *Client
}

// NewConversationSyncer creates a sync-mode extractor backed by client.
func NewConversationSyncer(client *Client) *ConversationSyncer {
	return &ConversationSyncer{client: client}
}

// Extract implements ctxpkg.Extractor. Best-effort: failures are logged,
// never returned — a sync failure must not be surfaced as a run error;
// the messages stay in the agent session store and the conversation can
// be re-synced manually.
func (s *ConversationSyncer) Extract(ctx context.Context, scope ctxpkg.ContextScope, messages []openagent.Message, runDelta []openagent.Message) {
	if s == nil || s.client == nil || len(runDelta) == 0 {
		return
	}
	payload := conversationPayload(runDelta, peerIDFor(scope))
	if len(payload) == 0 {
		return
	}
	ovSID := s.client.SessionIDFor(scope.SessionID)
	if _, err := s.client.AddMessages(ctx, payload, ovSID); err != nil {
		slog.Warn("openviking conversation sync failed", "ov_session", ovSID, "messages", len(payload), "error", err)
		return
	}
	if err := s.client.MaybeCommit(ctx, ovSID); err != nil {
		slog.Warn("openviking conversation commit check failed", "ov_session", ovSID, "error", err)
	}
	slog.Debug("openviking conversation synced", "ov_session", ovSID, "messages", len(payload))
}

// conversationPayload filters a run delta down to the text exchange:
// user messages (peer-attributed) and assistant prose. System messages,
// tool transport/result messages, and pure tool-call assistant turns
// carry no conversation semantics for the VLM (and tool output is
// bulky). Empty-content multimodal messages (image-only) are skipped —
// the batch API's simple mode is text.
func conversationPayload(runDelta []openagent.Message, peerID string) []MessagePayload {
	payload := make([]MessagePayload, 0, len(runDelta))
	for _, m := range runDelta {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		switch m.Role {
		case openagent.RoleUser:
			payload = append(payload, MessagePayload{Role: "user", Content: m.Content, PeerID: peerID})
		case openagent.RoleAssistant:
			// No peer_id: assistant content is the agent's own voice —
			// attribution belongs to the user messages.
			payload = append(payload, MessagePayload{Role: "assistant", Content: m.Content})
		}
	}
	return payload
}
