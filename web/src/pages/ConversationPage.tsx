import { useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import {
  BotIcon,
  ArrowLeftIcon,
  BriefcaseBusinessIcon,
  CheckIcon,
  ChevronRightIcon,
  InfoIcon,
  PencilLineIcon,
  ShieldCheckIcon,
  TriangleAlertIcon,
  UserIcon,
  PanelRightCloseIcon,
  PanelRightOpenIcon,
  ZapIcon,
} from 'lucide-react'
import {
  acknowledgeEscalation,
  getConversation,
  isAutoDecision,
  type Action,
  type Conversation,
  type ConversationDetail,
  type Message,
} from '../api'
import { ActionTicket } from '../components/ActionTicket'
import {
  AgentDraft,
  DismissedDraft,
  draftText,
  messageText,
  RevisedDraft,
  SupersededDraft,
} from '../components/AgentDraft'
import { CustomerComposer } from '../components/CustomerComposer'
import { DispatcherComposer } from '../components/DispatcherComposer'
import { JobPanel } from '../components/JobPanel'
import { TimeAgo } from '../components/TimeAgo'
import { Badge } from '@/components/ui/badge'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Button, buttonVariants } from '@/components/ui/button'
import {
  Message as MessageRow,
  MessageContent,
  MessageFooter,
} from '@/components/ui/message'
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from '@/components/ui/message-scroller'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Skeleton } from '@/components/ui/skeleton'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { cn } from '@/lib/utils'

type TimelineItem =
  | { kind: 'message'; at: string; message: Message }
  | { kind: 'action'; at: string; action: Action }

// An agent draft that hasn't been sent yet (awaiting a decision, or mid-send)
// has not reached the customer. When the dispatcher releases it, it lands
// after everything the customer has said so far — so that is where it belongs
// in the thread. Pinning it to its proposed-at time would wrongly float it
// above newer customer messages. A sent draft becomes an outbound message with
// a real delivery time, and a superseded (revised) draft is a settled record —
// both keep their chronological place.
const UNSENT_DRAFT_STATES = new Set<Action['state']>([
  'proposed',
  'pending_approval',
  'approved',
  'approved_with_edits',
  'executing',
])

function isUnsentDraft(action: Action): boolean {
  return (
	(action.tool === 'send_message' || action.tool === 'propose_response') &&
    draftText(action) != null &&
    UNSENT_DRAFT_STATES.has(action.state)
  )
}

// The thread interleaves customer/agent messages with the agent's action
// tickets in the order they happened — the dispatcher reads one review
// timeline, not two separate lists — except unsent drafts, which float to the
// bottom where they will actually be delivered.
function buildTimeline(messages: Message[], actions: Action[]): TimelineItem[] {
  const items: TimelineItem[] = [
    ...messages.map((m) => ({ kind: 'message' as const, at: m.created_at, message: m })),
    ...actions.map((a) => ({ kind: 'action' as const, at: a.proposed_at, action: a })),
  ]
  const byTime = (a: TimelineItem, b: TimelineItem) => a.at.localeCompare(b.at)
  // Settled events keep their real chronological order; unsent drafts are
  // appended after them (each group still ordered by time), so a draft always
  // sits below every message that precedes its eventual delivery.
  const isDraft = (i: TimelineItem) => i.kind === 'action' && isUnsentDraft(i.action)
  const settled = items.filter((i) => !isDraft(i)).sort(byTime)
  const drafts = items.filter(isDraft).sort(byTime)
  return [...settled, ...drafts]
}

// An outbound message is the record of a sent reply; the completed
// send_message action that produced it carries how it was released
// (auto-approved by policy vs a dispatcher decision). Pair them up by body
// text so the bubble can wear that provenance.
function matchSentActions(messages: Message[], actions: Action[]): Map<string, Action> {
  const sends = actions
	.filter((a) => (a.tool === 'send_message' || a.tool === 'propose_response') && a.state === 'completed')
    .sort((a, b) => a.proposed_at.localeCompare(b.proposed_at))
  const used = new Set<string>()
  const byMessage = new Map<string, Action>()
  // Only agent-authored outbound messages come from a send_message action;
  // dispatcher replies have no backing action, so they never match one.
  const outbound = messages
    .filter((m) => m.direction === 'outbound' && m.author === 'agent')
    .sort((a, b) => a.created_at.localeCompare(b.created_at))
  for (const message of outbound) {
    const match = sends.find((a) => !used.has(a.id) && draftText(a) === message.body)
    if (match) {
      used.add(match.id)
      byMessage.set(message.id, match)
    }
  }
  return byMessage
}

// A proposed reply is a message, so it renders as one: a draft bubble in
// the thread (AgentDraft). Once it's sent, the real outbound message is the
// record — its bubble wears the release stamp (and the pre-edit original,
// if any), so the action row would be a duplicate. Drop it. A draft that was
// decided but never sent stays in the thread as a settled record: "revised"
// (the dispatcher asked for a rewrite, fresh draft below) or "dismissed" (the
// dispatcher escaped it). Both land in the `rejected` state on the wire and
// are told apart by the decision kind. Failed sends and everything else stay
// work-order tickets.
function renderAction(action: Action, contextRevision: number) {
  if ((action.tool === 'send_message' || action.tool === 'propose_response') && draftText(action) != null) {
    switch (action.state) {
      case 'proposed':
      case 'pending_approval':
      case 'approved':
      case 'approved_with_edits':
      case 'executing':
		return <AgentDraft action={action} contextRevision={contextRevision} />
	  case 'superseded':
		return <SupersededDraft action={action} />
      case 'rejected':
        switch (action.decision?.kind) {
          case 'dismiss':
            return <DismissedDraft action={action} />
          case 'supersede':
            return <SupersededDraft action={action} />
          default:
            return <RevisedDraft action={action} />
        }
      case 'completed':
        return null
    }
  }
  return <ActionTicket action={action} contextRevision={contextRevision} />
}

// Run/channel internals live behind this popover: useful when something
// goes wrong, noise the rest of the time.
function DetailsPopover({ data }: { data: ConversationDetail }) {
  const rows: Array<[string, string]> = [
    ['Channel', data.conversation.channel_id],
    ['Status', data.conversation.status],
    ['Run', data.run ? `${data.run.status} · ${data.run.agent}` : '—'],
    ['Run ID', data.run?.id ?? '—'],
    ['Conversation ID', data.conversation.id],
  ]
  return (
    <Popover>
      <PopoverTrigger render={<Button variant="ghost" size="icon-sm" />}>
        <InfoIcon />
        <span className="sr-only">Conversation details</span>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <dl className="m-0 flex flex-col gap-2">
          {rows.map(([label, value]) => (
            <div key={label} className="flex items-baseline gap-2">
              <dt className="w-28 shrink-0 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                {label}
              </dt>
              <dd className="m-0 min-w-0 truncate font-mono text-xs" title={value}>
                {value}
              </dd>
            </div>
          ))}
        </dl>
      </PopoverContent>
    </Popover>
  )
}

export function ConversationPage() {
  const { conversationId = '' } = useParams({ strict: false })
  const [caseOpen,setCaseOpen]=useState(false)
  const [casePinned,setCasePinned]=useState(()=>localStorage.getItem('dispatch.case-panel-pinned')==='true')
  const { data, isLoading, error } = useQuery({
    queryKey: ['conversation', conversationId],
    queryFn: () => getConversation(conversationId),
    refetchInterval: 1500,
  })

  if (isLoading)
    return (
      <div className="flex flex-col gap-3 p-6">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-24 w-full max-w-2xl" />
        <Skeleton className="h-24 w-full max-w-2xl" />
      </div>
    )
  if (error || !data)
    return (
      <p className="p-6 text-sm text-destructive">
        {(error as Error)?.message ?? 'Not found'}
      </p>
    )

  const timeline = buildTimeline(data.messages ?? [], data.actions ?? [])
  const sentActions = matchSentActions(data.messages ?? [], data.actions ?? [])
  const closed = data.conversation.status === 'closed'
  const sourceMessageIds=(data.messages ?? []).filter(m=>m.direction==='inbound').slice(-5).map(m=>m.id)
  const setPinned=(pinned:boolean)=>{setCasePinned(pinned);localStorage.setItem('dispatch.case-panel-pinned',String(pinned));if(pinned)setCaseOpen(false)}
  const jobPanel=<JobPanel record={data.case} candidates={data.candidate_cases ?? []} conversationId={data.conversation.id} sourceMessageIds={sourceMessageIds} customerName={data.customer?.name} contact={data.contact} run={data.run} stage={data.current_stage} lastModel={data.last_model}/>

  return (
    <div className="flex h-full min-h-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex min-h-14 items-center gap-3 border-b bg-card px-3 py-2 sm:px-4">
          <Link to="/inbox" search={{filter:'all'}} aria-label="Back to Inbox" className={cn(buttonVariants({variant:'ghost',size:'icon-sm'}),'md:hidden')}><ArrowLeftIcon/><span className="sr-only">Back to Inbox</span></Link>
          <div className="min-w-0"><div className="flex items-baseline gap-2"><span className="truncate text-sm font-semibold">{data.customer?.name || data.contact || 'Unknown customer'}</span><span className="hidden shrink-0 font-mono text-[11px] text-muted-foreground sm:inline">{data.contact}</span></div><div className="mt-1 flex flex-wrap items-center gap-1.5">{data.current_stage&&<Badge variant="secondary">{data.current_stage.replace('_',' ')}</Badge>}{data.last_model&&<Badge variant="outline" className="hidden sm:inline-flex">{data.last_model}</Badge>}{data.current_draft&&<Badge variant="signal" className="pulse-soft">Awaiting decision</Badge>}{closed&&<Badge variant="outline" className="font-mono text-muted-foreground uppercase">closed</Badge>}</div></div>
          <div className="ml-auto flex shrink-0 items-center gap-1.5"><DetailsPopover data={data}/><Button variant="outline" size="sm" onClick={()=>setCaseOpen(true)} className={cn(casePinned&&'xl:hidden')}><BriefcaseBusinessIcon data-icon="inline-start"/><span className="hidden sm:inline">Customer &amp; cases</span></Button><Button variant="ghost" size="icon-sm" className="hidden xl:inline-flex" aria-label={casePinned?'Unpin customer and cases':'Pin customer and cases'} onClick={()=>setPinned(!casePinned)}>{casePinned?<PanelRightCloseIcon/>:<PanelRightOpenIcon/>}</Button></div>
        </div>

        <EscalationBanner conv={data.conversation} />

        <div className="min-h-0 flex-1">
          <MessageScrollerProvider>
            <MessageScroller>
              <MessageScrollerViewport>
                <MessageScrollerContent className="mx-auto w-full max-w-2xl gap-4 px-4 py-4">
                  {timeline.length === 0 && (
                    <p className="text-sm text-muted-foreground">No messages yet.</p>
                  )}
                  {timeline.map((item) => {
                    if (item.kind === 'message') {
                      return (
                        <MessageScrollerItem
                          key={item.message.id}
                          scrollAnchor={item.message.direction === 'inbound'}
                        >
                          <MessageBubble
                            message={item.message}
                            sentBy={sentActions.get(item.message.id)}
                          />
                        </MessageScrollerItem>
                      )
                    }
					const rendered = renderAction(item.action, data.conversation.context_revision)
                    if (!rendered) return null
                    return (
                      <MessageScrollerItem
                        key={item.action.id}
                        scrollAnchor={item.action.state === 'pending_approval'}
                      >
                        {rendered}
                      </MessageScrollerItem>
                    )
                  })}
                  <CustomerComposer
					connectionId={data.conversation.channel_id}
                    phone={data.contact}
                    name={data.customer?.name}
                    closed={closed}
                  />
				  <DispatcherComposer conversationId={data.conversation.id} contextRevision={data.conversation.context_revision} closed={closed} />
                </MessageScrollerContent>
              </MessageScrollerViewport>
              <MessageScrollerButton />
            </MessageScroller>
          </MessageScrollerProvider>
        </div>
      </div>

      {casePinned&&<aside className="hidden w-80 shrink-0 overflow-y-auto border-l p-3 xl:block">{jobPanel}</aside>}
      <Sheet open={caseOpen} onOpenChange={setCaseOpen}><SheetContent className="overflow-y-auto sm:max-w-md"><SheetHeader><SheetTitle>Customer &amp; cases</SheetTitle><SheetDescription>Customer context, the active case for this task, and customer-wide case history.</SheetDescription></SheetHeader><div className="px-4 pb-4">{jobPanel}</div></SheetContent></Sheet>
    </div>
  )
}

// The escalation banner sits above the thread when the agent has flagged the
// conversation for urgent human attention. Safety orange, per the design
// system, is reserved for exactly this: a decision a human owes right now.
// Acknowledging it is the dispatcher's "I've got this" — it records the
// engagement and clears the alarm to a calm, settled state.
function EscalationBanner({ conv }: { conv: Conversation }) {
  const queryClient = useQueryClient()
  const ack = useMutation({
    mutationFn: () => acknowledgeEscalation({ conversationId: conv.id }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['conversation', conv.id] })
      queryClient.invalidateQueries({ queryKey: ['conversations'] })
    },
  })

  if (conv.attention_state === 'flagged') {
    return (
      <div className="flex items-start gap-3 border-b border-signal/30 bg-signal/10 px-4 py-3">
        <TriangleAlertIcon className="mt-0.5 size-5 shrink-0 text-signal" />
        <div className="min-w-0 flex-1">
          <p className="font-mono text-[11px] font-semibold tracking-widest text-signal uppercase">
            Escalated — needs a dispatcher now
          </p>
          <p className="mt-0.5 text-sm text-foreground">
            {conv.attention_reason || 'The agent flagged this conversation for urgent attention.'}
          </p>
        </div>
        <Button
          variant="signal"
          size="sm"
          className="shrink-0"
          onClick={() => ack.mutate()}
          disabled={ack.isPending}
        >
          <CheckIcon data-icon="inline-start" />
          {ack.isPending ? 'Acknowledging…' : 'Acknowledge'}
        </Button>
      </div>
    )
  }

  if (conv.attention_state === 'acknowledged') {
    return (
      <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-2 text-xs text-muted-foreground">
        <ShieldCheckIcon className="size-4 shrink-0 text-ok" />
        <span>
          Escalation acknowledged by dispatcher
          {conv.attention_reason ? ` — ${conv.attention_reason}` : ''}
        </span>
      </div>
    )
  }

  return null
}

function MessageBubble({ message, sentBy }: { message: Message; sentBy?: Action }) {
  const inbound = message.direction === 'inbound'
  if (inbound) {
    return (
      <MessageRow align="start">
        <MessageContent>
          <Bubble variant="muted" align="start">
            <BubbleContent className="whitespace-pre-wrap">{message.body}</BubbleContent>
          </Bubble>
          <MessageFooter className="gap-1.5">
            <span className="font-mono text-[10px] tracking-widest uppercase">Customer</span>
            <TimeAgo at={message.created_at} />
          </MessageFooter>
        </MessageContent>
      </MessageRow>
    )
  }
  // A dispatcher's own reply to the customer — a first-class human message, not
  // an agent send. It wears a plain human stamp and carries no agent provenance.
  if (message.author === 'dispatcher') {
    return (
      <MessageRow align="end">
        <MessageContent>
          <Bubble variant="default" align="end">
            <BubbleContent className="p-0">
              <DispatcherStamp />
              <div className="px-3 py-2 whitespace-pre-wrap">{message.body}</div>
            </BubbleContent>
          </Bubble>
          <MessageFooter>
            <TimeAgo at={message.created_at} />
			<DeliveryLabel message={message} />
          </MessageFooter>
        </MessageContent>
      </MessageRow>
    )
  }
  const original =
    sentBy?.decision?.kind === 'approve_with_edits' ? messageText(sentBy.input) : null
  return (
    <MessageRow align="end">
      <MessageContent>
        <Bubble variant="default" align="end">
          <BubbleContent className="p-0">
            <AgentStamp action={sentBy} />
            <div className="px-3 py-2 whitespace-pre-wrap">{message.body}</div>
          </BubbleContent>
        </Bubble>
        {original != null && original !== message.body && <OriginalDraft text={original} />}
        <MessageFooter>
          <TimeAgo at={message.created_at} />
		  <DeliveryLabel message={message} />
        </MessageFooter>
      </MessageContent>
    </MessageRow>
  )
}

function DeliveryLabel({ message }: { message: Message }) {
  if (message.direction === 'inbound' || message.delivery_state === 'sent') return null
  const blocked = message.delivery_state === 'failed' || message.delivery_state === 'unknown'
  return <Badge variant={blocked ? 'destructive' : 'outline'}>{message.delivery_state}</Badge>
}

// An edited reply keeps the agent's pre-edit text one click away — the
// bubble stays the record of what the customer actually received.
function OriginalDraft({ text }: { text: string }) {
  const [open, setOpen] = useState(false)
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="flex max-w-[80%] flex-col items-end self-end"
    >
      <CollapsibleTrigger
        render={
          <Button variant="ghost" size="xs" className="font-mono text-muted-foreground" />
        }
      >
        <ChevronRightIcon
          data-icon="inline-start"
          className={cn('transition-transform', open && 'rotate-90')}
        />
        Agent&rsquo;s original
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-1 rounded-xl border border-dashed px-3 py-2 text-sm leading-relaxed text-muted-foreground whitespace-pre-wrap">
          {text}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

// The stamp at the top of a dispatcher's own reply: a human sent this straight
// to the customer, no agent involved.
function DispatcherStamp() {
  return (
    <div className="flex items-center gap-1.5 border-b border-primary-foreground/15 bg-primary-foreground/10 px-3 py-1 font-mono text-[10px] tracking-widest text-primary-foreground/80 uppercase [&_svg]:size-3 [&_svg]:shrink-0">
      <UserIcon />
      <span>Dispatcher</span>
    </div>
  )
}

// The stamp at the top of a sent bubble: the agent wrote this, and here is
// how it was released — automatically under policy, or by a dispatcher
// decision (with or without edits).
function AgentStamp({ action }: { action?: Action }) {
  let release: { icon: ReactNode; text: string } | null = null
  if (action) {
    if (!action.decision || isAutoDecision(action.decision)) {
      release = { icon: <ZapIcon />, text: 'sent automatically' }
    } else if (action.decision.kind === 'approve_with_edits') {
      release = { icon: <PencilLineIcon />, text: `edited by ${action.decision.decided_by}` }
    } else {
      release = { icon: <CheckIcon />, text: `approved by ${action.decision.decided_by}` }
    }
  }
  return (
    <div className="flex items-center gap-1.5 border-b border-primary-foreground/15 bg-primary-foreground/10 px-3 py-1 font-mono text-[10px] tracking-widest text-primary-foreground/80 uppercase [&_svg]:size-3 [&_svg]:shrink-0">
      <BotIcon />
      <span>Agent</span>
      {release && (
        <>
          <span className="text-primary-foreground/40">·</span>
          {release.icon}
          <span className="truncate">{release.text}</span>
        </>
      )}
    </div>
  )
}
