import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { BotIcon, RotateCcwIcon } from 'lucide-react'
import { decideAction, type Action } from '../api'
import { TimeAgo } from './TimeAgo'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Button } from '@/components/ui/button'
import {
  Message,
  MessageContent,
  MessageFooter,
} from '@/components/ui/message'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

/** The message text of a send_message payload, or null. */
export function messageText(input: unknown): string | null {
  if (input == null || typeof input !== 'object' || Array.isArray(input)) return null
  const message = (input as Record<string, unknown>).message
  return typeof message === 'string' ? message : null
}

/** The proposed outbound text of a send_message action (edited copy wins), or null. */
export function draftText(action: Action): string | null {
  return messageText(action.edited_input ?? action.input)
}

// When the dispatcher sends a draft back for a revision, the superseded draft
// stays in the thread as a quiet, settled record — not a harsh rejection. It
// keeps the draft bubble's shape with the strip flipped to "revised", and the
// dispatcher's instruction quoted underneath, so you can read what you asked
// for against the fresh draft the agent produced below.
//
// On the wire this is still a `reject` decision: the agent loop already treats
// a rejection as "revise, don't repeat", so "revise" is the honest name for
// what the mechanism does. A true veto / take-over — where the agent stands
// down and the dispatcher writes the reply themselves — waits on the dispatcher
// composer and is deliberately not offered yet.
export function RevisedDraft({ action }: { action: Action }) {
  const text = draftText(action) ?? ''
  return (
    <Message align="end">
      <MessageContent>
        <Bubble variant="outline" align="end" className="max-w-full">
          <BubbleContent className="border-dashed bg-muted/40 p-0">
            <div className="flex items-center gap-1.5 border-b border-dashed px-3 py-1 font-mono text-[10px] tracking-widest text-muted-foreground uppercase [&_svg]:size-3 [&_svg]:shrink-0">
              <BotIcon />
              <span>Agent draft</span>
              <span className="opacity-50">·</span>
              <RotateCcwIcon />
              <span>revised</span>
            </div>
            <div className="px-3 py-2 text-muted-foreground whitespace-pre-wrap">{text}</div>
          </BubbleContent>
        </Bubble>
        {action.decision?.reason && (
          <p className="m-0 max-w-[80%] self-end px-3 text-right text-xs text-muted-foreground">
            You asked the agent to revise: “{action.decision.reason}”
          </p>
        )}
        <MessageFooter>
          <TimeAgo at={action.proposed_at} />
        </MessageFooter>
      </MessageContent>
    </Message>
  )
}

// A send_message proposal is a message the business hasn't sent yet, so it
// reads like one: an outbound bubble in the thread — dashed while it's a
// draft — with the decision keys underneath instead of a ticket card.
export function AgentDraft({ action }: { action: Action }) {
  const qc = useQueryClient()
  const [mode, setMode] = useState<'idle' | 'edit' | 'revise'>('idle')
  const [draft, setDraft] = useState('')
  const [reason, setReason] = useState('')

  const decide = useMutation({
    mutationFn: decideAction,
    onSuccess: () => {
      setMode('idle')
      qc.invalidateQueries()
    },
  })

  const pending = action.state === 'pending_approval'
  const text = draftText(action) ?? ''

  return (
    <Message align="end">
      <MessageContent>
        <Bubble variant="outline" align="end" className="max-w-full">
          <BubbleContent className="border-dashed border-signal/60 bg-signal-soft/30 p-0">
            <div
              className={cn(
                'flex items-center gap-1.5 border-b border-dashed border-signal/40 px-3 py-1 font-mono text-[10px] tracking-widest uppercase [&_svg]:size-3 [&_svg]:shrink-0',
                pending ? 'text-signal' : 'text-muted-foreground',
              )}
            >
              <BotIcon />
              <span>Agent draft</span>
              <span className="opacity-50">·</span>
              {pending ? (
                <>
                  <span className="pulse-soft size-1.5 shrink-0 rounded-full bg-signal" />
                  <span>needs your review</span>
                </>
              ) : (
                <>
                  <Spinner />
                  <span>sending…</span>
                </>
              )}
            </div>
            {mode === 'edit' ? (
              <div className="flex w-96 max-w-full flex-col gap-2 p-2">
                <Textarea
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  rows={3}
                  autoFocus
                  className="bg-card text-sm"
                />
                <div className="flex justify-end gap-2">
                  <Button size="sm" variant="ghost" onClick={() => setMode('idle')}>
                    Cancel
                  </Button>
                  <Button
                    size="sm"
                    onClick={() =>
                      draft.trim() &&
                      decide.mutate({
                        actionId: action.id,
                        kind: 'approve_with_edits',
                        editedInput: {
                          ...(action.input as Record<string, unknown>),
                          message: draft.trim(),
                        },
                      })
                    }
                    disabled={decide.isPending || !draft.trim()}
                  >
                    {decide.isPending && <Spinner data-icon="inline-start" />}
                    Send edited
                  </Button>
                </div>
              </div>
            ) : (
              <div className={cn('px-3 py-2 whitespace-pre-wrap', !pending && 'opacity-80')}>
                {text}
              </div>
            )}
          </BubbleContent>

          {pending && mode === 'idle' && (
            <div className="flex justify-end gap-2 pt-1">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setMode('revise')}
              >
                Revise…
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setDraft(text)
                  setMode('edit')
                }}
              >
                Edit…
              </Button>
              <Button
                size="sm"
                onClick={() => decide.mutate({ actionId: action.id, kind: 'approve' })}
                disabled={decide.isPending}
              >
                {decide.isPending && <Spinner data-icon="inline-start" />}
                Send
              </Button>
            </div>
          )}

          {pending && mode === 'revise' && (
            <div className="flex w-96 max-w-full flex-col gap-2 pt-1">
              <Textarea
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                rows={2}
                autoFocus
                className="bg-card text-sm"
                placeholder="Tell the agent what to change — it rewrites the draft."
              />
              <div className="flex justify-end gap-2">
                <Button size="sm" variant="ghost" onClick={() => setMode('idle')}>
                  Cancel
                </Button>
                <Button
                  size="sm"
                  onClick={() =>
                    reason.trim() &&
                    decide.mutate({ actionId: action.id, kind: 'reject', reason })
                  }
                  disabled={decide.isPending || !reason.trim()}
                >
                  {decide.isPending && <Spinner data-icon="inline-start" />}
                  Ask agent to revise
                </Button>
              </div>
            </div>
          )}

          {decide.isError && (
            <p className="m-0 pt-1 text-right text-xs text-destructive">
              {(decide.error as Error).message}
            </p>
          )}
        </Bubble>
        <MessageFooter>
          <TimeAgo at={action.proposed_at} />
        </MessageFooter>
      </MessageContent>
    </Message>
  )
}
