import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { exactTime, timeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'

// Relative time is what a dispatcher scans; the exact timestamp is one
// hover away.
export function TimeAgo({ at, className }: { at: string; className?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span className={cn('cursor-default font-mono text-[11px] text-muted-foreground', className)} />
        }
      >
        {timeAgo(at)}
      </TooltipTrigger>
      <TooltipContent>{exactTime(at)}</TooltipContent>
    </Tooltip>
  )
}
