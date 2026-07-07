import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { PlusIcon } from 'lucide-react'
import { listConversations } from '../api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'

export function ConversationList() {
  const { data } = useQuery({
    queryKey: ['conversations'],
    queryFn: listConversations,
    refetchInterval: 3000,
  })
  const conversations = data?.conversations ?? []

  return (
    <div>
      <div className="flex items-center justify-between px-4 pt-3 pb-2">
        <span className="font-mono text-[11px] font-medium tracking-widest text-muted-foreground uppercase">
          Conversations
        </span>
        <Link to="/">
          <Button variant="outline" size="xs">
            <PlusIcon data-icon="inline-start" />
            New
          </Button>
        </Link>
      </div>
      {conversations.length === 0 && (
        <p className="px-4 py-3 text-sm text-muted-foreground">
          No conversations yet. Start one as a customer.
        </p>
      )}
      <ul>
        {conversations.map((c) => (
          <li key={c.conversation.id}>
            <Link
              to="/conversations/$conversationId"
              params={{ conversationId: c.conversation.id }}
              className="block border-b px-4 py-3 hover:bg-muted/60"
              activeProps={{ className: 'bg-muted/60' }}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="truncate text-sm font-semibold">
                  {c.customer?.name || c.customer?.phone || 'Unknown'}
                </span>
                {c.pending_count > 0 && (
                  <Badge variant="signal" className="pulse-soft shrink-0 font-mono">
                    {c.pending_count} to review
                  </Badge>
                )}
                {c.conversation.status === 'closed' && c.pending_count === 0 && (
                  <Badge variant="outline" className="shrink-0 font-mono text-muted-foreground">
                    closed
                  </Badge>
                )}
              </div>
              <div className="mt-0.5 flex items-baseline justify-between gap-2">
                <span className="truncate text-xs text-muted-foreground">
                  {c.last_message
                    ? `${c.last_message.direction === 'inbound' ? '' : '↩ '}${c.last_message.body}`
                    : '—'}
                </span>
                <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                  {c.customer?.phone}
                </span>
              </div>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  )
}
