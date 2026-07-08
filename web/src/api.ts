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

export type DecisionKind =
  | 'approve'
  | 'approve_with_edits'
  | 'reject'
  | 'dismiss'
  // Recorded by the workflow when a dispatcher replies to the customer directly
  // while a draft is pending — the draft is withdrawn, not sent. Never sent from
  // the client (design/003-dispatcher-as-participant.md).
  | 'supersede'

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

// Customer is the CRM aggregate. Contact endpoints (phone, email) live on
// contact identities, not here; a thread's contact address is surfaced as
// `contact` on the conversation summary/detail (design/004-domain-remodel.md §3).
export interface Customer {
  id: string
  name: string
}

export type AttentionState = 'none' | 'flagged' | 'acknowledged'

export interface Conversation {
  id: string
  customer_id: string
  channel_id: string
  run_id?: string
  status: 'open' | 'closed'
  attention_state: AttentionState
  attention_reason?: string
  escalated_at?: string
  created_at: string
  updated_at: string
}

export type MessageAuthor = 'customer' | 'agent' | 'dispatcher'

export interface Message {
  id: string
  conversation_id: string
  direction: 'inbound' | 'outbound'
  author: MessageAuthor
  body: string
  created_at: string
}

// Case is the unit of work — the generalization of a field-service "job"
// (design/004-domain-remodel.md §5). Typed core + a per-vertical `data` bag
// whose schema the playbook owns (field service: address / issue / urgency).
export interface Case {
  id: string
  conversation_id: string
  customer_id: string
  type: string
  status: 'intake' | 'intake_complete'
  data: Record<string, string>
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
  contact: string // customer's address on this thread's channel (design/004 §3)
  last_message?: Message
  pending_count: number
}

export interface ConversationDetail {
  conversation: Conversation
  customer: Customer | null
  contact: string // customer's address on this thread's channel (design/004 §3)
  messages: Message[] | null
  case?: Case
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
  return request<{ conversation_id: string; run_id: string }>('/api/dev/inbound', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

// sendDispatcherReply posts a message the dispatcher types straight to the
// customer — a first-class participant reply, not an agent action
// (design/003-dispatcher-as-participant.md).
export function sendDispatcherReply(input: { conversationId: string; text: string }) {
  return request<{ status: string; message_id: string }>(
    `/api/conversations/${input.conversationId}/reply`,
    {
      method: 'POST',
      body: JSON.stringify({ text: input.text, sent_by: 'dispatcher' }),
    },
  )
}

export function acknowledgeEscalation(input: { conversationId: string; note?: string }) {
  return request<{ status: string; conversation_id: string }>(
    `/api/conversations/${input.conversationId}/acknowledge`,
    {
      method: 'POST',
      body: JSON.stringify({ acknowledged_by: 'dispatcher', note: input.note }),
    },
  )
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
