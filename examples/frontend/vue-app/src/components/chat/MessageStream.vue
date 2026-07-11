<template>
  <div class="chat-view">
    <div class="msg-area" ref="scrollRef">
      <n-empty v-if="messages.length === 0" description="Send a message to get started" class="empty-state" />
      <template v-for="item in displayItems" :key="item.kind === 'msg' ? item.msg.id : item.id">
        <!-- Tool batch (rendered before individual tools would appear) -->
        <div v-if="item.kind === 'tool_batch'" class="msg-tool-batch">
          <n-collapse>
            <n-collapse-item :title="`🔧 Tool calls (${item.tools.length})`">
              <div class="tc-list">
                <n-collapse v-for="(t, i) in item.tools" :key="i">
                  <n-collapse-item :title="t.name" class="tc-sub">
                    <pre class="tc-args">{{ t.args }}</pre>
                    <pre v-if="t.result" class="tc-result">{{ t.result.length > 2000 ? t.result.slice(-2000) : t.result }}</pre>
                  </n-collapse-item>
                </n-collapse>
              </div>
            </n-collapse-item>
          </n-collapse>
        </div>

        <!-- Handoff -->
        <div v-else-if="item.kind === 'msg' && item.msg.role === 'handoff'" class="msg-handoff">
          <div class="handoff-label">{{ item.msg.content }}</div>
        </div>

        <!-- System -->
        <div v-else-if="item.kind === 'msg' && item.msg.role === 'system'" class="sys-msg">{{ item.msg.content }}</div>

        <!-- Thought -->
        <n-collapse v-else-if="item.kind === 'msg' && item.msg.role === 'thought'" class="msg-thought">
          <n-collapse-item title="Thinking...">
            <MarkdownContent :content="item.msg.content" />
          </n-collapse-item>
        </n-collapse>

        <!-- Agent -->
        <div v-else-if="item.kind === 'msg' && item.msg.role === 'agent'" class="msg-agent">
          <div v-if="item.msg.agent" class="agent-label">{{ item.msg.agent }}</div>
          <div v-if="item.msg.thoughtContent" class="thought-inline">
            <n-collapse>
              <n-collapse-item title="Thinking...">
                <div class="thought-text">{{ item.msg.thoughtContent }}</div>
              </n-collapse-item>
            </n-collapse>
          </div>
          <div class="agent-body">
            <MarkdownContent :content="item.msg.content" />
            <span v-if="item.msg.isStreaming" class="cursor">▌</span>
          </div>
        </div>

        <!-- User -->
        <div v-else-if="item.kind === 'msg' && item.msg.role === 'user'" class="msg-user">
          <div class="user-body">{{ item.msg.content }}</div>
        </div>
      </template>

      <div v-if="error" class="error-msg">{{ error }}</div>
    </div>

    <UsageBar :usage="usage" />

    <div class="input-area">
      <n-input
        v-model:value="inputText"
        type="textarea"
        :autosize="{ minRows: 1, maxRows: 5 }"
        placeholder="Type a message... (Enter to send, Shift+Enter for newline)"
        :disabled="disabled"
        @keydown="onKeydown"
      />
      <n-button type="primary" :disabled="!inputText.trim() || disabled" @click="send" class="send-btn">Send</n-button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, nextTick, watch, computed } from 'vue'
import { NEmpty, NCollapse, NCollapseItem, NInput, NButton } from 'naive-ui'
import type { ChatMessage, UsageInfo } from '@/types'
import MarkdownContent from '@/components/common/MarkdownContent.vue'
import UsageBar from '@/components/chat/UsageBar.vue'

const props = defineProps<{
  messages: ChatMessage[]
  usage: UsageInfo | null
  error: string | null
  disabled?: boolean
}>()

const emit = defineEmits<{ send: [text: string] }>()

const inputText = ref('')
const scrollRef = ref<HTMLElement | null>(null)

watch(() => props.messages.length, () => {
  nextTick(() => {
    if (scrollRef.value) scrollRef.value.scrollTop = scrollRef.value.scrollHeight
  })
})

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
}
function send() {
  const t = inputText.value.trim()
  if (!t || props.disabled) return
  emit('send', t)
  inputText.value = ''
}

interface ToolBatchItem {
  name: string
  args: string
  result: string
}

type DisplayItem =
  | { kind: 'msg'; msg: ChatMessage }
  | { kind: 'tool_batch'; tools: ToolBatchItem[]; id: string }

// Group consecutive tool_call/tool_result messages into batches.
// Each tool message carries toolCall (name + args) and content (result).
// Store is unchanged — this is a pure rendering transform.
const displayItems = computed<DisplayItem[]>(() => {
  const items: DisplayItem[] = []
  let batch: ToolBatchItem[] = []

  function flush() {
    if (batch.length > 0) {
      items.push({ kind: 'tool_batch', tools: [...batch], id: items.length.toString() })
      batch = []
    }
  }

  for (const m of props.messages) {
    const isTool = m.role === 'tool_call' || m.role === 'tool_result'
    if (!isTool) {
      flush()
      items.push({ kind: 'msg', msg: m })
      continue
    }
    // Build tool item from the message (toolCall = original call info,
    // content = args at creation time, mutated to result on tool_result).
    if (m.toolCall) {
      const item: ToolBatchItem = {
        name: m.toolCall.function.name,
        args: (() => {
          try { return JSON.stringify(JSON.parse(m.toolCall.function.arguments), null, 2) }
          catch { return m.toolCall.function.arguments }
        })(),
        result: m.role === 'tool_result' ? m.content : '',
      }
      batch.push(item)
    }
  }
  flush()
  return items
})

function truncate(s: string): string {
  return s.length > 10000 ? s.slice(0, 10000) + '\n\n... (truncated)' : s
}
</script>

<style scoped>
.chat-view {
  height: 100%; display: flex; flex-direction: column; overflow: hidden;
}

.msg-area {
  flex: 1; overflow-y: auto; padding: 16px 0;
  display: flex; flex-direction: column; gap: 6px;
}
.empty-state { margin-top: 80px; }

.sys-msg {
  text-align: center; font-size: 0.7em; opacity: 0.3;
  padding: 2px 12px;
}

.msg-handoff {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 16px; margin: 4px 0;
}
.handoff-label {
  font-size: 0.8em; font-weight: 600; opacity: 0.65;
  padding: 6px 14px; border-radius: 6px;
  background: rgba(99, 102, 241, 0.12);
  border: 1px solid rgba(99, 102, 241, 0.25);
  color: rgba(255,255,255,0.75);
  text-transform: uppercase; letter-spacing: 0.04em;
  white-space: nowrap;
}

.thought-inline { margin-bottom: 8px; opacity: 0.6; font-size: 0.85em; }
.thought-text { font-size: 0.9em; white-space: pre-wrap; word-break: break-word; max-height: 200px; overflow-y: auto; }
.msg-thought, .msg-tool-batch { margin: 4px 16px; }

.tc-list { display: flex; flex-direction: column; gap: 2px; }

.tc-sub :deep(.n-collapse-item__header) {
  font-size: 0.73em;
  opacity: 0.55;
}

.tc-args {
  font-size: 0.71em; white-space: pre-wrap; word-break: break-word;
  color: rgba(255,255,255,0.35); line-height: 1.35;
  max-height: 150px; overflow-y: auto; margin: 0;
}

.tc-result {
  font-size: 0.71em; white-space: pre-wrap; word-break: break-word;
  color: rgba(255,255,255,0.4); line-height: 1.35;
  max-height: 200px; overflow-y: auto;
  margin: 6px 0 0; padding: 6px 8px;
  background: rgba(0,0,0,0.15); border-radius: 4px;
}

.msg-agent { padding: 4px 16px; max-width: 85%; align-self: flex-start; }
.agent-label {
  font-size: 0.72em; font-weight: 700; opacity: 0.55; margin-bottom: 2px;
  text-transform: uppercase; letter-spacing: 0.04em;
}
.agent-body { font-size: 0.92em; line-height: 1.6; }
.cursor { animation: blink 1s step-end infinite; }
@keyframes blink { 50% { opacity: 0; } }

.msg-user { padding: 4px 16px; max-width: 75%; align-self: flex-end; }
.user-body {
  background: #2563eb; color: #fff;
  padding: 10px 16px; border-radius: 18px 18px 4px 18px;
  font-size: 0.92em; line-height: 1.5;
}

.input-area {
  display: flex; gap: 8px; padding: 12px 16px;
  border-top: 1px solid rgba(255,255,255,0.08);
  align-items: flex-end; flex-shrink: 0;
}
.send-btn { flex-shrink: 0; }

.error-msg {
  background: rgba(239, 68, 68, 0.12); color: #ef4444;
  padding: 10px 16px; margin: 4px 16px; border-radius: 6px; font-size: 0.85em;
}
</style>
