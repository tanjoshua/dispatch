import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ChevronRightIcon } from 'lucide-react'
import { decideAction, isAutoDecision, type Action } from '../api'
import { TimeAgo } from './TimeAgo'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { prettyJson, summarize, toolLabel } from '@/lib/format'
import { cn } from '@/lib/utils'

// The badge answers "how was this released?" — the HITL story of the
// ticket. Policy releases read "auto-approved"; human releases read
// "approved", with edits noted.
function stateBadge(action: Action): {
  text: string
  variant: 'signal' | 'ok' | 'destructive' | 'outline'
} {
  const auto = !action.decision || isAutoDecision(action.decision)
  switch (action.state) {
    case 'pending_approval':
      return { text: 'needs decision', variant: 'signal' }
    case 'completed':
    case 'approved':
      if (auto) return { text: 'auto-approved', variant: 'ok' }
      return action.decision?.kind === 'approve_with_edits'
        ? { text: 'approved · edited', variant: 'ok' }
        : { text: 'approved', variant: 'ok' }
    case 'approved_with_edits':
      return { text: 'approved · edited', variant: 'ok' }
    case 'rejected':
      return { text: 'rejected', variant: 'destructive' }
    case 'failed':
      return { text: 'failed', variant: 'destructive' }
    default:
      return { text: action.state, variant: 'outline' }
  }
}

// The proposal rendered for humans: labeled fields first, raw JSON one
// click deeper.
function ProposalSummary({ input, rawLabel }: { input: unknown; rawLabel: string }) {
  const rows = summarize(input)
  return (
    <div className="flex flex-col gap-1">
      {rows ? (
        <dl className="m-0">
          {rows.map(({ label, value }) => (
            <div key={label} className="flex gap-2 py-0.5">
              <dt className="w-24 shrink-0 pt-0.5 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                {label}
              </dt>
              <dd className="m-0 min-w-0 text-sm whitespace-pre-wrap">{value}</dd>
            </div>
          ))}
        </dl>
      ) : (
        <p className="m-0 text-sm whitespace-pre-wrap">{prettyJson(input)}</p>
      )}
      {rows && <RawPayload label={rawLabel} value={input} />}
    </div>
  )
}

function RawPayload({ label, value }: { label: string; value: unknown }) {
  const [open, setOpen] = useState(false)
  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger
        render={
          <Button variant="ghost" size="xs" className="-ml-1.5 font-mono text-muted-foreground" />
        }
      >
        <ChevronRightIcon
          data-icon="inline-start"
          className={cn('transition-transform', open && 'rotate-90')}
        />
        {label}
      </CollapsibleTrigger>
      <CollapsibleContent>
        <pre className="mt-1 overflow-x-auto rounded-md bg-muted px-2.5 py-2 font-mono text-xs whitespace-pre-wrap">
          {prettyJson(value)}
        </pre>
      </CollapsibleContent>
    </Collapsible>
  )
}

// ActionTicket renders one agent action as a work-order ticket. Pending
// tickets are fully expanded and carry the decision keys; decided tickets
// collapse to one audit line, with the full record (original proposal,
// edits, decision, result) behind the chevron.
export function ActionTicket({ action, contextRevision }: { action: Action; contextRevision: number }) {
  const qc = useQueryClient()
  const [mode, setMode] = useState<'idle' | 'edit' | 'reject'>('idle')
  const [draft, setDraft] = useState('')
  const [draftError, setDraftError] = useState<string | null>(null)
  const [reason, setReason] = useState('')

  const decide = useMutation({
    mutationFn: decideAction,
    onSuccess: () => {
      setMode('idle')
      qc.invalidateQueries()
    },
  })

  const pending = action.state === 'pending_approval'
  const failed = action.state === 'failed'
  const [detailsOpen, setDetailsOpen] = useState(failed)
  const badge = stateBadge(action)

  const header = (
    <div className="flex min-w-0 items-center gap-2">
      <span className="truncate text-sm font-semibold" title={action.tool}>
        {toolLabel(action.tool)}
      </span>
      <Badge variant={badge.variant} className={cn('font-mono uppercase', pending && 'pulse-soft')}>
        {badge.text}
      </Badge>
      <TimeAgo at={action.proposed_at} className="ml-auto shrink-0" />
    </div>
  )

  const auto = isAutoDecision(action.decision) || (!action.decision && action.state === 'completed')
  const decisionNote = auto ? (
    <p className="m-0 text-xs text-muted-foreground">
      Auto-approved by policy — ran without a dispatcher decision.
    </p>
  ) : action.decision ? (
    <p className="m-0 text-xs text-muted-foreground">
      {action.decision.kind === 'reject' ? 'Rejected' : 'Approved'} by{' '}
      <span className="font-mono">{action.decision.decided_by}</span>
      {action.decision.reason ? ` — ${action.decision.reason}` : ''}
    </p>
  ) : null

  // Decided tickets stay quiet but not mute: the header says how the action
  // was released, a short field preview says what it did, and the full audit
  // record (all fields, decision, raw payload, result) sits behind the
  // chevron.
  if (!pending) {
    const rows = summarize(action.edited_input ?? action.input)
    const previewRows = rows?.slice(0, 3)
    const moreCount = (rows?.length ?? 0) - (previewRows?.length ?? 0)
    return (
      <Card className="gap-0 py-0">
        <Collapsible open={detailsOpen} onOpenChange={setDetailsOpen}>
          <CollapsibleTrigger
            render={<button type="button" className="block w-full px-4 py-2.5 text-left" />}
          >
            <div className="flex min-w-0 items-center gap-2">
              <ChevronRightIcon
                className={cn(
                  'size-3.5 shrink-0 text-muted-foreground transition-transform',
                  detailsOpen && 'rotate-90',
                )}
              />
              {header}
            </div>
            {!detailsOpen && previewRows && (
              <dl className="m-0 mt-1.5 flex flex-col gap-0.5 pl-[22px]">
                {previewRows.map(({ label, value }) => (
                  <div key={label} className="flex items-baseline gap-2">
                    <dt className="w-24 shrink-0 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                      {label}
                    </dt>
                    <dd className="m-0 min-w-0 truncate text-xs">{value}</dd>
                  </div>
                ))}
                {moreCount > 0 && (
                  <div className="font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                    +{moreCount} more
                  </div>
                )}
              </dl>
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="flex flex-col gap-3 border-t px-4 py-3">
              {action.edited_input != null ? (
                <>
                  <div>
                    <p className="m-0 mb-1 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                      What ran (dispatcher-edited)
                    </p>
                    <ProposalSummary input={action.edited_input} rawLabel="Raw payload" />
                  </div>
                  <RawPayload label="Agent's original proposal" value={action.input} />
                </>
              ) : (
                <ProposalSummary input={action.input} rawLabel="Raw payload" />
              )}
              {decisionNote}
              {action.error && (
                <p className="m-0 text-xs text-destructive">Error: {action.error}</p>
              )}
              {action.result != null && <RawPayload label="Result" value={action.result} />}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </Card>
    )
  }

  return (
    <Card className="gap-0 border-l-2 border-l-signal py-0">
      <CardHeader className="block border-b px-4 py-2.5">{header}</CardHeader>
      <CardContent className="px-4 py-3">
        <ProposalSummary input={action.input} rawLabel="Raw payload" />
      </CardContent>
      <CardFooter className="flex-col items-stretch gap-2 border-t px-4 py-3">
        {mode === 'idle' && (
          <div className="flex gap-2">
            <Button
              size="sm"
			  onClick={() => decide.mutate({ actionId: action.id, expectedActionVersion: action.version, expectedContextRevision: contextRevision, kind: 'approve' })}
              disabled={decide.isPending}
            >
              {decide.isPending && <Spinner data-icon="inline-start" />}
              Approve
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setDraft(prettyJson(action.input))
                setDraftError(null)
                setMode('edit')
              }}
            >
              Edit…
            </Button>
            <Button size="sm" variant="destructive" onClick={() => setMode('reject')}>
              Reject…
            </Button>
          </div>
        )}

        {mode === 'edit' && (
          <>
            <Textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              rows={6}
              aria-invalid={draftError != null}
              className="font-mono text-xs"
            />
            {draftError && <p className="m-0 text-xs text-destructive">{draftError}</p>}
            <div className="flex gap-2">
              <Button
                size="sm"
                onClick={() => {
                  try {
                    const parsed = JSON.parse(draft)
                  decide.mutate({
                    actionId: action.id,
					expectedActionVersion: action.version,
					expectedContextRevision: contextRevision,
                      kind: 'approve_with_edits',
                      editedInput: parsed,
                    })
                  } catch {
                    setDraftError('Edited input must be valid JSON.')
                  }
                }}
                disabled={decide.isPending}
              >
                {decide.isPending && <Spinner data-icon="inline-start" />}
                Approve with edits
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setMode('idle')}>
                Cancel
              </Button>
            </div>
          </>
        )}

        {mode === 'reject' && (
          <>
            <Input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Why is this wrong? The agent reads this and revises."
            />
            <div className="flex gap-2">
              <Button
                size="sm"
                variant="destructive"
                onClick={() =>
				  reason.trim() && decide.mutate({ actionId: action.id, expectedActionVersion: action.version, expectedContextRevision: contextRevision, kind: 'reject', reason })
                }
                disabled={decide.isPending || !reason.trim()}
              >
                {decide.isPending && <Spinner data-icon="inline-start" />}
                Reject
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setMode('idle')}>
                Cancel
              </Button>
            </div>
          </>
        )}

        {decide.isError && (
          <p className="m-0 text-xs text-destructive">{(decide.error as Error).message}</p>
        )}
      </CardFooter>
    </Card>
  )
}
