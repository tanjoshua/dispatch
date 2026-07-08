import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { SendHorizontalIcon } from 'lucide-react'
import { sendDispatcherReply } from '../api'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'

// DispatcherComposer is the dispatcher's own voice in the conversation. It sits
// at the foot of the thread on the outbound (right) side, always available:
// there is no "agent's turn" to wait for and no takeover to trigger — the
// dispatcher can reply to the customer at any time (design/003). The message
// goes out the same path the agent's replies do, and the agent sees it in
// context; if a draft was pending, sending here supersedes it.
export function DispatcherComposer({
  conversationId,
  closed,
}: {
  conversationId: string
  closed: boolean
}) {
  const qc = useQueryClient()
  const [text, setText] = useState('')

  const send = useMutation({
    mutationFn: () => sendDispatcherReply({ conversationId, text: text.trim() }),
    onSuccess: () => {
      setText('')
      qc.invalidateQueries()
    },
  })

  const submit = () => {
    if (!text.trim() || send.isPending) return
    send.mutate()
  }

  if (closed) {
    return (
      <div className="mt-2 self-end rounded-xl border border-dashed bg-muted/30 px-3 py-2 text-[11px] text-muted-foreground">
        This conversation is closed. A new customer message starts a fresh run.
      </div>
    )
  }

  return (
    <div className="mt-2 flex w-full max-w-[85%] flex-col gap-2 self-end rounded-xl border bg-card p-2.5">
      <span className="px-0.5 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
        Reply as dispatcher
      </span>
      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            submit()
          }
        }}
        rows={2}
        placeholder="Message the customer directly…"
        className="max-h-40 min-h-9 resize-none bg-background"
      />
      <div className="flex items-center justify-end gap-2">
        {send.isError && (
          <p className="mr-auto text-[11px] text-destructive">{(send.error as Error).message}</p>
        )}
        <Button size="sm" onClick={submit} disabled={send.isPending || !text.trim()}>
          {send.isPending ? (
            <Spinner data-icon="inline-start" />
          ) : (
            <SendHorizontalIcon data-icon="inline-start" />
          )}
          Send
        </Button>
      </div>
    </div>
  )
}
