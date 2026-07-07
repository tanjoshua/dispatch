import { Outlet } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { listConversations } from '../api'
import { ConversationList } from './ConversationList'
import { Badge } from '@/components/ui/badge'

export function Layout() {
  const { data } = useQuery({
    queryKey: ['conversations'],
    queryFn: listConversations,
    refetchInterval: 3000,
  })
  const pendingTotal = (data?.conversations ?? []).reduce(
    (sum, c) => sum + c.pending_count,
    0,
  )

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
        {pendingTotal > 0 && (
          <Badge variant="signal" className="pulse-soft ml-auto font-mono">
            {pendingTotal} awaiting decision
          </Badge>
        )}
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
