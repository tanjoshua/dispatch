import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams } from '@tanstack/react-router'
import {
  BotIcon,
  CheckIcon,
  ChevronRightIcon,
  InfoIcon,
  MessageSquarePlusIcon,
  PencilLineIcon,
  ZapIcon,
} from 'lucide-react'
import {
  getConversation,
  isAutoDecision,
  type Action,
  type ConversationDetail,
  type Message,
} from '../api'
import { ActionTicket } from '../components/ActionTicket'
import { AgentDraft, draftText, messageText, RejectedDraft } from '../components/AgentDraft'
import { JobPanel } from '../components/JobPanel'
import { Simulator } from '../components/Simulator'
import { TimeAgo } from '../components/TimeAgo'
import { Badge } from '@/components/ui/badge'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Button } from '@/components/ui/button'
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

type TimelineItem =
  | { kind: 'message'; at: string; message: Message }
  | { kind: 'action'; at: string; action: Action }

// The thread interleaves customer/agent messages with the agent's action
// tickets in the order they happened — the dispatcher reads one review
// timeline, not two separate lists.
function buildTimeline(messages: Message[], actions: Action[]): TimelineItem[] {
  const items: TimelineItem[] = [
    ...messages.map((m) => ({ kind: 'message' as const, at: m.created_at, message: m })),
    ...actions.map((a) => ({ kind: 'action' as const, at: a.proposed_at, action: a })),
  ]
  return items.sort((a, b) => a.at.localeCompare(b.at))
}

// An outbound message is the record of a sent reply; the completed
// send_message action that produced it carries how it was released
// (auto-approved by policy vs a dispatcher decision). Pair them up by body
// text so the bubble can wear that provenance.
function matchSentActions(messages: Message[], actions: Action[]): Map<string, Action> {
  const sends = actions
    .filter((a) => a.tool === 'send_message' && a.state === 'completed')
    .sort((a, b) => a.proposed_at.localeCompare(b.proposed_at))
  const used = new Set<string>()
  const byMessage = new Map<string, Action>()
  const outbound = messages
    .filter((m) => m.direction === 'outbound')
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
// if any), so the action row would be a duplicate. Drop it. A rejected
// reply stays in the thread as a dead draft with the reason attached
// (RejectedDraft). Failed sends and everything else stay work-order
// tickets.
function renderAction(action: Action) {
  if (action.tool === 'send_message' && draftText(action) != null) {
    switch (action.state) {
      case 'proposed':
      case 'pending_approval':
      case 'approved':
      case 'approved_with_edits':
      case 'executing':
        return <AgentDraft action={action} />
      case 'rejected':
        return <RejectedDraft action={action} />
      case 'completed':
        return null
    }
  }
  return <ActionTicket action={action} />
}

// Run/channel internals live behind this popover: useful when something
// goes wrong, noise the rest of the time.
function DetailsPopover({ data }: { data: ConversationDetail }) {
  const rows: Array<[string, string]> = [
    ['Channel', data.conversation.channel],
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

function SimulatorSheet({ phone, closed }: { phone?: string; closed: boolean }) {
  return (
    <Sheet>
      <SheetTrigger render={<Button variant="outline" size="sm" />}>
        <MessageSquarePlusIcon data-icon="inline-start" />
        Simulate customer
      </SheetTrigger>
      <SheetContent side="right">
        <SheetHeader>
          <SheetTitle>Customer simulator</SheetTitle>
          <SheetDescription>
            Sends a message through the same inbound path a WhatsApp webhook would.
            {closed && ' This conversation is closed, so a new message starts a fresh run.'}
          </SheetDescription>
        </SheetHeader>
        <div className="px-4">
          <Simulator phone={phone} />
        </div>
      </SheetContent>
    </Sheet>
  )
}

export function ConversationPage() {
  const { conversationId } = useParams({ from: '/conversations/$conversationId' })
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

  return (
    <div className="flex h-full min-h-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="flex items-center gap-3 border-b bg-card px-4 py-2">
          <span className="text-sm font-semibold">
            {data.customer?.name || data.customer?.phone || 'Unknown customer'}
          </span>
          <span className="font-mono text-[11px] text-muted-foreground">
            {data.customer?.phone}
          </span>
          {closed && (
            <Badge variant="outline" className="font-mono text-muted-foreground uppercase">
              closed
            </Badge>
          )}
          <div className="ml-auto flex items-center gap-1.5">
            <SimulatorSheet phone={data.customer?.phone} closed={closed} />
            <DetailsPopover data={data} />
          </div>
        </div>

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
                    const rendered = renderAction(item.action)
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
                </MessageScrollerContent>
              </MessageScrollerViewport>
              <MessageScrollerButton />
            </MessageScroller>
          </MessageScrollerProvider>
        </div>
      </div>

      <aside className="w-80 shrink-0 overflow-y-auto border-l p-3">
        <JobPanel job={data.job} run={data.run} />
      </aside>
    </div>
  )
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
        </MessageFooter>
      </MessageContent>
    </MessageRow>
  )
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
