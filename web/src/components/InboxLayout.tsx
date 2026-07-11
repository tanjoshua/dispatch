import { Outlet, useRouterState } from '@tanstack/react-router'
import { ConversationList } from '@/components/ConversationList'
import { cn } from '@/lib/utils'

export function InboxLayout() {
  const detail = useRouterState({ select: (state) => state.location.pathname !== '/inbox' })

  return (
    <div className="flex h-full min-w-0">
      <aside
        className={cn(
          'h-full overflow-y-auto bg-card md:w-80 md:shrink-0 md:border-r lg:w-96',
          detail ? 'hidden md:block' : 'w-full',
        )}
      >
        <ConversationList />
      </aside>
      <section className={cn('h-full min-w-0 flex-1', !detail && 'hidden md:block')}>
        <Outlet />
      </section>
    </div>
  )
}
