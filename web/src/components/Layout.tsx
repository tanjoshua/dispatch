import { Link, Outlet } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ChartColumnIcon } from 'lucide-react'
import { listConversations } from '../api'
import { ConversationList } from './ConversationList'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { waitingFor } from '@/lib/format'

export function Layout() {
  const { data } = useQuery({
    queryKey: ['conversations'],
    queryFn: listConversations,
    refetchInterval: 3000,
  })
  const conversations = data?.conversations ?? []
  const pendingTotal = conversations.reduce((sum, c) => sum + c.pending_count, 0)
  // The queue's worst wait, worn in the header: decision latency is the
  // number this product lives or dies on.
  const oldestPending = conversations
    .map((c) => c.oldest_pending_at)
    .filter((at): at is string => !!at)
    .sort()[0]

  return (
    <div className="flex h-full flex-col">
      <header className="flex items-center gap-3 border-b bg-card px-5 py-2.5">
        <span aria-hidden className="size-2.5 rounded-[2px] bg-signal" />
        <span className="font-heading text-sm font-bold tracking-[0.2em] uppercase">
          Dispatch
        </span>
        <span className="hidden text-sm text-muted-foreground sm:block">
          The intake agent proposes, you decide.
        </span>
        <span className="ml-auto flex items-center gap-2">
          {pendingTotal > 0 && (
            <Badge variant="signal" className="pulse-soft font-mono">
              {pendingTotal} awaiting decision
              {oldestPending ? ` · ${waitingFor(oldestPending)}` : ''}
            </Badge>
          )}
          <Link to="/stats">
            <Button variant="ghost" size="xs" aria-label="Decision stats">
              <ChartColumnIcon data-icon="inline-start" />
              Stats
            </Button>
          </Link>
        </span>
      </header>
      <div className="flex min-h-0 flex-1">
        <aside className="w-72 shrink-0 overflow-y-auto border-r bg-card">
          <ConversationList />
        </aside>
        <main className="min-w-0 flex-1 overflow-hidden">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
