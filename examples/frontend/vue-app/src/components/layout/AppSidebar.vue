<template>
  <n-layout-sider bordered collapse-mode="width" :width="260" class="sidebar">
    <div class="sidebar-inner">
      <div class="mode-tabs">
        <n-button-group>
          <n-button
            v-for="m in modes" :key="m.key"
            :type="currentMode === m.key ? 'primary' : 'default'"
            size="small"
            @click="switchMode(m.key)"
          >{{ m.label }}</n-button>
        </n-button-group>
      </div>

      <div class="list-header">
        <n-text depth="3" class="list-title">SESSIONS</n-text>
        <n-button size="tiny" circle secondary @click="handleCreate">
          <template #icon><span style="font-size:16px;line-height:1">+</span></template>
        </n-button>
      </div>

      <div class="session-scroll">
        <n-spin :show="store.loading" size="small">
          <n-empty v-if="!store.loading && sessions.length === 0" description="No sessions" style="margin-top:40px" />
          <div
            v-for="s in sessions" :key="s.id"
            class="session-item"
            :class="{ active: s.id === currentId }"
            @click="selectSession(s.id)"
          >
            <div class="session-info">
              <div v-if="editingId !== s.id" class="session-title" @dblclick.stop="startEdit(s)">{{ s.title || 'untitled' }}</div>
              <input
                v-else
                ref="editInput"
                v-model="editTitle"
                class="session-title-input"
                @keydown.enter="saveEdit(s.id)"
                @keydown.escape="cancelEdit"
                @blur="saveEdit(s.id)"
                @click.stop
              />
              <div class="session-meta">
                <span v-if="s.modelId" class="meta-tag model">{{ s.modelId }}</span>
              </div>
            </div>
            <n-button size="tiny" text class="delete-btn" @click.stop="handleDelete(s.id)">
              <template #icon><span style="font-size:14px">&times;</span></template>
            </n-button>
          </div>
        </n-spin>
      </div>

      <PipelinePanel ref="pipelineRef" />
    </div>
  </n-layout-sider>
</template>

<script setup lang="ts">
import { computed, watch, ref, onMounted, nextTick } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { NLayoutSider, NButton, NButtonGroup, NSpin, NEmpty, NText, NEllipsis } from 'naive-ui'
import { useSessionsStore, type AgentMode } from '@/stores/sessions'
import { useChatStore } from '@/stores/chat'
import * as api from '@/api'
import type { SessionInfo } from '@/types'
import PipelinePanel from '@/components/pipeline/PipelinePanel.vue'

const router = useRouter()
const route = useRoute()
const store = useSessionsStore()
const chat = useChatStore()
const pipelineRef = ref<InstanceType<typeof PipelinePanel> | null>(null)
const currentId = ref<string | null>(null)
const editingId = ref<string | null>(null)
const editTitle = ref('')

const modes = [
  { key: 'single' as AgentMode, label: 'Chat' },
  { key: 'team' as AgentMode, label: 'Team' },
  { key: 'plan' as AgentMode, label: 'Plan' },
]

const currentMode = computed<AgentMode>(() => {
  const p = route.path
  if (p.startsWith('/team')) return 'team'
  if (p.startsWith('/plan')) return 'plan'
  return 'single'
})

const sessions = computed(() => store.sessionsFor(currentMode.value).value)

watch(currentMode, async (mode) => {
  chat.clearChat()
  store.currentSessionId = null // clear before fetch so old sessionId doesn't leak
  await store.fetchSessions(mode)
  const s = sessions.value
  if (s.length > 0) {
    store.selectSession(s[0].id)
    router.push(`/${mode}/${s[0].id}`)
  } else {
    await handleCreate()
  }
}, { immediate: true })

watch(currentId, () => { pipelineRef.value?.reset() })

onMounted(() => {
  chat.setStageHandler((evt: any) => {
    pipelineRef.value?.handleStage(evt.stage)
  })
})
watch(() => route.params.sessionId, (id) => {
  if (id && typeof id === 'string') currentId.value = id
})

function switchMode(mode: AgentMode) { router.push(`/${mode}`) }
function selectSession(id: string) {
  currentId.value = id
  store.selectSession(id)
  router.push(`/${currentMode.value}/${id}`)
}
async function handleCreate() {
  try {
    const info = await store.createSession(currentMode.value)
    router.push(`/${currentMode.value}/${info.id}`)
  } catch { /* store handles it */ }
}
async function handleDelete(id: string) {
  await store.deleteSession(id, currentMode.value)
  if (currentId.value === id) router.push(`/${currentMode.value}`)
}

function startEdit(s: SessionInfo) {
  editingId.value = s.id
  editTitle.value = s.title || ''
  nextTick(() => {
    const el = document.querySelector<HTMLInputElement>('.session-title-input')
    el?.focus()
    el?.select()
  })
}
function cancelEdit() { editingId.value = null }
async function saveEdit(id: string) {
  if (editingId.value !== id) return
  const title = editTitle.value.trim()
  editingId.value = null
  if (!title) return
  try {
    const m = currentMode.value
    if (m === 'single') await api.updateSession(id, title)
    else if (m === 'team') await api.updateTeamSession(id, title)
    else if (m === 'plan') await api.updatePlanSession(id, title)
    // Update the session in the local list.
    const s = sessions.value.find(x => x.id === id)
    if (s) { s.title = title; s.updatedAt = new Date().toISOString() }
  } catch { /* ignore */ }
}
</script>

<style scoped>
.sidebar { height: 100%; overflow: hidden !important; }
.sidebar-inner { height: 100%; display: flex; flex-direction: column; }
.mode-tabs { padding: 12px; display: flex; justify-content: center; flex-shrink: 0; }
.list-header {
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 14px 4px; flex-shrink: 0;
}
.list-title { font-size: 0.75em; letter-spacing: 0.05em; }
.session-scroll { flex: 1; overflow-y: auto; min-height: 0; }
.session-item {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 14px; margin: 2px 6px; border-radius: 6px;
  cursor: pointer; transition: background 0.15s;
}
.session-item:hover { background: var(--n-color-pressed); }
.session-item.active { background: color-mix(in srgb, var(--n-color-target) 20%, transparent); }
.session-info { flex: 1; min-width: 0; }
.session-title { font-size: 0.88em; cursor: text; }
.session-title-input {
  font-size: 0.88em; width: 100%; background: rgba(255,255,255,0.06);
  border: 1px solid rgba(255,255,255,0.15); border-radius: 4px;
  padding: 2px 6px; color: inherit; outline: none;
}
.session-meta { display: flex; gap: 4px; margin-top: 2px; }
.meta-tag { font-size: 0.68em; opacity: 0.45; padding: 0 4px; border-radius: 3px; background: rgba(255,255,255,0.05); }
.delete-btn { opacity: 0; transition: opacity 0.15s; }
.session-item:hover .delete-btn { opacity: 1; }
</style>
