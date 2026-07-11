import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { PlusIcon, TriangleAlertIcon } from 'lucide-react'
import { listConversations } from '../api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { waitingFor } from '@/lib/format'
import { cn } from '@/lib/utils'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

export function ConversationList() {
	const navigate=useNavigate();const filter=useRouterState({select:s=>new URLSearchParams(s.location.searchStr).get('filter')??'all'})
  const { data } = useQuery({
    queryKey: ['conversations'],
    queryFn: listConversations,
    refetchInterval: 3000,
  })
  const conversations = (data?.conversations ?? []).filter(c=>filter==='needs_decision'?c.pending_count>0:filter==='escalated'?c.conversation.attention_state==='flagged':true).sort((a,b)=>{const ae=a.conversation.attention_state==='flagged',be=b.conversation.attention_state==='flagged';if(ae!==be)return ae?-1:1;return (a.oldest_pending_at??'z').localeCompare(b.oldest_pending_at??'z')})

  return (
    <div>
      <div className="flex items-center justify-between px-4 pt-3 pb-2">
        <span className="font-mono text-[11px] font-medium tracking-widest text-muted-foreground uppercase">
          Conversations
        </span>
        <Link to="/inbox">
          <Button variant="outline" size="xs">
            <PlusIcon data-icon="inline-start" />
            New
          </Button>
        </Link>
      </div>
      <div className="px-3 pb-2"><ToggleGroup value={[filter]} onValueChange={v=>v[0]&&navigate({to:'/inbox',search:{filter:v[0] as 'all'|'needs_decision'|'escalated'}})} variant="outline" size="sm" spacing={0} className="w-full"><ToggleGroupItem value="needs_decision">Needs decision</ToggleGroupItem><ToggleGroupItem value="escalated">Escalated</ToggleGroupItem><ToggleGroupItem value="all">All</ToggleGroupItem></ToggleGroup></div>
      {conversations.length === 0 && (
        <p className="px-4 py-3 text-sm text-muted-foreground">
          No conversations yet. Start one as a customer.
        </p>
      )}
      <ul>
        {conversations.map((c) => {
          const flagged = c.conversation.attention_state === 'flagged'
          return (
            <li key={c.conversation.id}>
              <Link
                to="/inbox/$conversationId"
                params={{ conversationId: c.conversation.id }}
                className={cn(
                  'block border-b px-4 py-3 hover:bg-muted/60',
                  // Safety orange, reserved for "a human decision is needed
                  // now": a flagged conversation wears a left accent.
                  flagged && 'border-l-2 border-l-signal bg-signal/5 hover:bg-signal/10',
                )}
                activeProps={{ className: 'bg-muted/60' }}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate text-sm font-semibold">
                    {c.customer?.name || c.contact || 'Unknown'}
                  </span>
                  {flagged && (
                    <Badge variant="signal" className="pulse-soft shrink-0 font-mono uppercase">
                      <TriangleAlertIcon data-icon="inline-start" />
                      Escalated
                    </Badge>
                  )}
                  {!flagged && c.pending_count > 0 && (
                    <Badge variant="signal" className="pulse-soft shrink-0 font-mono">
                      {c.pending_count} to review
                      {c.oldest_pending_at ? ` · ${waitingFor(c.oldest_pending_at)}` : ''}
                    </Badge>
                  )}
                  {!flagged && c.conversation.status === 'closed' && c.pending_count === 0 && (
                    <Badge variant="outline" className="shrink-0 font-mono text-muted-foreground">
                      closed
                    </Badge>
                  )}
                </div>
                <div className="mt-0.5 flex items-baseline justify-between gap-2">
                  <span
                    className={cn(
                      'truncate text-xs text-muted-foreground',
                      flagged && 'font-medium text-signal',
                    )}
                  >
                    {flagged
                      ? c.conversation.attention_reason || 'Needs a dispatcher now'
                      : c.last_message
                        ? `${c.last_message.direction === 'inbound' ? '' : '↩ '}${c.last_message.body}`
                        : '—'}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                    {c.contact}
                  </span>
                </div>
              </Link>
            </li>
          )
        })}
      </ul>
    </div>
  )
}
