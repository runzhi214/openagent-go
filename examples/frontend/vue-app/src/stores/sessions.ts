import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { SessionInfo } from '@/types'
import * as api from '@/api'

export type AgentMode = 'single' | 'team' | 'plan'

const emptyList: SessionInfo[] = []

export const useSessionsStore = defineStore('sessions', () => {
  const singleSessions = ref<SessionInfo[]>([])
  const teamSessions   = ref<SessionInfo[]>([])
  const planSessions   = ref<SessionInfo[]>([])

  const currentSessionId = ref<string | null>(null)
  const loading = ref(false)

  function sessionsFor(mode: AgentMode) {
    switch (mode) {
      case 'single': return singleSessions
      case 'team':   return teamSessions
      case 'plan':   return planSessions
    }
  }

  const currentSession = computed(() => {
    const all = [...singleSessions.value, ...teamSessions.value, ...planSessions.value]
    return all.find(s => s.id === currentSessionId.value) ?? null
  })

  async function fetchSessions(mode: AgentMode) {
    loading.value = true
    try {
      let list: SessionInfo[]
      switch (mode) {
        case 'single': list = await api.listSessions(); break
        case 'team':   list = await api.listTeamSessions(); break
        case 'plan':   list = await api.listPlanSessions(); break
      }
      sessionsFor(mode).value = list
    } catch (e) {
      console.error('fetchSessions:', e)
      sessionsFor(mode).value = []
    } finally {
      loading.value = false
    }
  }

  async function createSession(mode: AgentMode, title?: string) {
    try {
      let info: SessionInfo
      switch (mode) {
        case 'single': info = await api.createSession({ title }); break
        case 'team':   info = await api.createTeamSession({ title }); break
        case 'plan':   info = await api.createPlanSession({ title }); break
      }
      sessionsFor(mode).value.unshift(info)
      currentSessionId.value = info.id
      return info
    } catch (e) {
      console.error('createSession:', e)
      throw e
    }
  }

  async function deleteSession(id: string, mode: AgentMode) {
    try {
      switch (mode) {
        case 'single': await api.deleteSession(id); break
        case 'team':   await api.deleteTeamSession(id); break
        case 'plan':   await api.deletePlanSession(id); break
      }
      const s = sessionsFor(mode)
      s.value = s.value.filter(x => x.id !== id)
      if (currentSessionId.value === id) {
        currentSessionId.value = null
      }
    } catch (e) {
      console.error('deleteSession:', e)
    }
  }

  function selectSession(id: string) {
    currentSessionId.value = id
  }

  return {
    sessionsFor, currentSessionId, currentSession, loading,
    fetchSessions, createSession, deleteSession, selectSession,
  }
})
