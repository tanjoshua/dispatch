import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { MessageCirclePlusIcon, SendHorizontalIcon } from 'lucide-react'
import { sendInbound } from '../api'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'

// CustomerComposer is the dev's way to speak as the customer. It lives on the
// customer's side of the thread (left), right under the latest message: a
// collapsed affordance you click to reveal an inline text box, so a simulated
// customer message appears exactly where a real one would. The dispatcher's own
// reply composer (DispatcherComposer) sits just below it, on the outbound side.
// Sending hits the same inbound path a real WhatsApp webhook would — nothing
// downstream knows the difference.
export function CustomerComposer({
  phone,
  name,
  closed,
}: {
  phone?: string
  name?: string
  closed: boolean
}) {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')

  const send = useMutation({
    mutationFn: sendInbound,
    onSuccess: () => {
      setText('')
      setOpen(false)
      qc.invalidateQueries()
    },
  })

  const submit = () => {
    const body = text.trim()
    if (!body || !phone) return
    send.mutate({ phone, name: name ?? '', text: body })
  }

  const cancel = () => {
    setText('')
    setOpen(false)
  }

  if (!open) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex items-center gap-2 self-start rounded-xl border border-dashed border-border bg-transparent px-3 py-2 text-sm text-muted-foreground transition-colors hover:border-foreground/30 hover:bg-muted hover:text-foreground"
      >
        <MessageCirclePlusIcon className="size-4" />
        {closed ? 'Message as the customer — starts a new conversation' : 'Message as the customer'}
      </button>
    )
  }

  return (
    <div className="flex w-full max-w-[85%] flex-col gap-2 self-start rounded-xl border bg-card p-2.5">
      <span className="flex items-center gap-1.5 px-0.5 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
        You&rsquo;re the customer
        {(name || phone) && (
          <span className="tracking-normal text-muted-foreground/70 normal-case">
            · {name || phone}
          </span>
        )}
      </span>
      <Textarea
        autoFocus
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault()
            submit()
          } else if (e.key === 'Escape') {
            cancel()
          }
        }}
        rows={2}
        placeholder="Type as the customer…"
        className="max-h-40 min-h-9 resize-none bg-background"
      />
      {closed && (
        <p className="px-0.5 text-[11px] text-muted-foreground">
          This conversation is closed — your message starts a fresh run.
        </p>
      )}
      <div className="flex items-center justify-end gap-2">
        {send.isError && (
          <p className="mr-auto text-[11px] text-destructive">{(send.error as Error).message}</p>
        )}
        <Button variant="ghost" size="sm" onClick={cancel} disabled={send.isPending}>
          Cancel
        </Button>
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
