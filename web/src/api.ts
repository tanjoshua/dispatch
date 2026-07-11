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
  | 'superseded'

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
	version: number
	context_revision: number
	responds_through_event_seq?: number
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
	contact_identity_id: string
	event_seq: number
	context_revision: number
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
	event_seq?: number
	delivery_state: 'queued' | 'sending' | 'sent' | 'failed' | 'unknown'
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
	version: number
	summary: string
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
  // When the longest-waiting pending action was proposed. Decision latency is
  // the existential product risk — the age is worn on the row.
  oldest_pending_at?: string
}

export interface ConversationDetail {
  conversation: Conversation
  customer: Customer | null
  contact: string // customer's address on this thread's channel (design/004 §3)
  messages: Message[] | null
  case?: Case
	candidate_cases: Case[]
	current_draft?: Action
  run?: Run
  current_stage?: string
  last_model?: string
  actions: Action[] | null
}

// ToolDecisionStats mirrors agentkit.ToolDecisionStats: per-tool decision
// outcomes and human-decision latency — the evidence the autonomy slider
// (RequireApproval → AutoApprove) moves on.
export interface ToolDecisionStats {
  tool: string
  proposed: number
  auto_approved: number
  approved: number // human, without edits
  approved_with_edits: number
  rejected: number
  dismissed: number
  superseded: number
  pending: number
  oldest_pending_at?: string
  avg_decision_seconds?: number
  median_decision_seconds?: number
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => null)
    throw new ApiError(body?.error ?? `${res.status} ${res.statusText}`, res.status, body)
  }
  return res.json()
}

export class ApiError extends Error {
  status: number
  body: unknown
  constructor(message: string, status: number, body: unknown) { super(message); this.status = status; this.body = body }
}

export type PolicyValue = 'auto' | 'require_review' | 'forbid'
export interface PackStage { id:string;label:string;description:string;model:string;status:'live'|'coming_soon' }
export interface PackTool { name: string; label: string; risk: string; default: PolicyValue; settings: PolicyValue[] }
export interface PackBlock { id: string; label: string; status: 'live' | 'coming_soon' }
export interface PackLane { id: string; label: string; description: string; status: 'live' | 'coming_soon'; blocks: PackBlock[]; tools?: PackTool[] }
export interface Pack { id: string; label: string; description: string; agent_name: string; lanes: PackLane[]; stages:PackStage[];default_model:string;catalog:{label:string;status:string;description:string} }
export interface VoiceConfig { agent_name: string; tone: string; custom_instructions: string }
export interface PlaybookConfig { schema:number;pack:string;models?:{per_stage?:Record<string,{override?:string}>};voice:VoiceConfig;policy:Record<string,Record<string,PolicyValue>> }
export interface Playbook { id:string;org_id:string;name:string;agent:string;case_type:string;config:PlaybookConfig;version:number;created_at:string }
export interface EffectiveTool {value:PolicyValue;source:string;locked:boolean}
export interface EffectiveConfig { config:PlaybookConfig;policy:Record<string,Record<string,EffectiveTool>>;model:string;models:Record<string,string> }
export interface ProfileFact {id:string;label:string;text:string}
export interface OrgProfile {business_name:string;hours:string;service_area:string;facts:ProfileFact[]}
export interface ChannelConnection {id:string;org_id:string;kind:string;address:string;status:string;default_playbook_id:string;version:number;created_at:string}
export interface ChannelKind {id:string;label:string;status:'available'|'coming_soon';description:string}

export function getPacks(){return request<{packs:Pack[]}>('/api/packs')}
export function listPlaybooks(){return request<{playbooks:Playbook[]}>('/api/playbooks')}
export function getPlaybook(id:string){return request<{playbook:Playbook;effective:EffectiveConfig}>(`/api/playbooks/${id}`)}
export function updatePlaybookConfig(playbook:Playbook,config:PlaybookConfig){return request<{playbook:Playbook;effective:EffectiveConfig}>(`/api/playbooks/${playbook.id}`,{method:'PATCH',body:JSON.stringify({command_id:crypto.randomUUID(),expected_version:playbook.version,config})})}
export function getOrgProfile(){return request<{profile:OrgProfile;version:number}>('/api/org/profile')}
export function updateOrgProfile(profile:OrgProfile,expectedVersion:number){return request<{profile:OrgProfile;version:number}>('/api/org/profile',{method:'PATCH',body:JSON.stringify({command_id:crypto.randomUUID(),expected_version:expectedVersion,profile})})}
export function listChannels(){return request<{connections:ChannelConnection[];kinds:ChannelKind[]}>('/api/channels')}
export function createChannel(input:{kind:string;address:string;defaultPlaybookId:string}){const commandId=crypto.randomUUID();return request<ChannelConnection>('/api/channels',{method:'POST',body:JSON.stringify({command_id:commandId,kind:input.kind,address:input.address,default_playbook_id:input.defaultPlaybookId})})}
export function updateChannel(connection:ChannelConnection,defaultPlaybookId:string){return request<ChannelConnection>(`/api/channels/${connection.id}`,{method:'PATCH',body:JSON.stringify({command_id:crypto.randomUUID(),expected_version:connection.version,default_playbook_id:defaultPlaybookId})})}

export function listConversations() {
  return request<{ conversations: ConversationSummary[] }>('/api/conversations')
}

export function getConversation(id: string) {
  return request<ConversationDetail>(`/api/conversations/${id}`)
}

export function getDecisionStats() {
  return request<{ tools: ToolDecisionStats[] }>('/api/stats/decisions')
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
export function sendDispatcherReply(input: { conversationId: string; text: string; expectedContextRevision: number; commandId: string }) {
  return request<{ status: string; message_id: string }>(
    `/api/conversations/${input.conversationId}/reply`,
    {
      method: 'POST',
	  body: JSON.stringify({ text: input.text, command_id: input.commandId, expected_context_revision: input.expectedContextRevision }),
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

export function correctCase(input: { conversationId: string; caseRecord: Case; patch: Record<string,string>; sourceMessageIds: string[] }) {
	return request<Case>(`/api/conversations/${input.conversationId}/cases/${input.caseRecord.id}/correction`, {method:'POST',body:JSON.stringify({expected_version:input.caseRecord.version,patch:input.patch,source_message_ids:input.sourceMessageIds})})
}

export function decideAction(input: {
  actionId: string
	expectedActionVersion: number
	expectedContextRevision: number
  kind: DecisionKind
  editedInput?: unknown
  reason?: string
}) {
  return request<{ status: string }>(`/api/actions/${input.actionId}/decision`, {
    method: 'POST',
    body: JSON.stringify({
	  decision_id: crypto.randomUUID(),
	  expected_action_version: input.expectedActionVersion,
	  expected_context_revision: input.expectedContextRevision,
      kind: input.kind,
      edited_input: input.editedInput,
      reason: input.reason,
    }),
  })
}
