// Typed client for the Go server's JSON API. The API is the contract —
// these types mirror the Go structs' JSON serialization.

export type ActionState =
  | 'proposed'
  | 'pending_approval'
  | 'approved'
  | 'approved_with_edits'
  | 'rejected'
  | 'executing'
  | 'completed'
  | 'failed'

export type DecisionKind = 'approve' | 'approve_with_edits' | 'reject'

export interface Decision {
  kind: DecisionKind
  decided_by: string
  reason: string
}

// Mirrors agentkit's DecidedByPolicy: the approval policy released this
// action without a human decision.
export const DECIDED_BY_POLICY = 'policy:auto'

export function isAutoDecision(decision?: Decision): boolean {
  return decision != null && decision.decided_by === DECIDED_BY_POLICY
}

export interface Action {
  id: string
  org_id: string
  run_id: string
  tool_call_id: string
  tool: string
  input: unknown
  edited_input?: unknown
  state: ActionState
  decision?: Decision
  result?: unknown
  error?: string
  proposed_at: string
  decided_at?: string
  executed_at?: string
}

export interface Customer {
  id: string
  phone: string
  name: string
}

export interface Conversation {
  id: string
  customer_id: string
  channel: string
  run_id?: string
  status: 'open' | 'closed'
  created_at: string
  updated_at: string
}

export interface Message {
  id: string
  conversation_id: string
  direction: 'inbound' | 'outbound'
  body: string
  created_at: string
}

export interface Job {
  id: string
  conversation_id: string
  customer_name: string
  phone: string
  address: string
  issue: string
  urgency: string
  status: 'intake' | 'intake_complete'
  updated_at: string
}

export interface Run {
  id: string
  agent: string
  status: 'running' | 'completed' | 'failed'
}

export interface ConversationSummary {
  conversation: Conversation
  customer: Customer | null
  last_message?: Message
  pending_count: number
}

export interface ConversationDetail {
  conversation: Conversation
  customer: Customer | null
  messages: Message[] | null
  job?: Job
  run?: Run
  actions: Action[] | null
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new Error(body?.error ?? `${res.status} ${res.statusText}`)
  }
  return res.json()
}

export function listConversations() {
  return request<{ conversations: ConversationSummary[] }>('/api/conversations')
}

export function getConversation(id: string) {
  return request<ConversationDetail>(`/api/conversations/${id}`)
}

export function sendInbound(input: { phone: string; name: string; text: string }) {
  return request<{ conversation_id: string; run_id: string }>('/api/simulate/inbound', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export function decideAction(input: {
  actionId: string
  kind: DecisionKind
  editedInput?: unknown
  reason?: string
}) {
  return request<{ status: string }>(`/api/actions/${input.actionId}/decision`, {
    method: 'POST',
    body: JSON.stringify({
      kind: input.kind,
      edited_input: input.editedInput,
      reason: input.reason,
      decided_by: 'dispatcher',
    }),
  })
}
