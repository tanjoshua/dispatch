import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { sendInbound } from '../api'
import { Button } from '@/components/ui/button'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'

// Simulator is the stand-in for a real WhatsApp customer. It hits the same
// inbound path a webhook would; nothing downstream knows the difference.
export function Simulator({
  phone: fixedPhone,
  onStarted,
}: {
  phone?: string
  onStarted?: (conversationId: string) => void
}) {
  const qc = useQueryClient()
  const [phone, setPhone] = useState(
    fixedPhone ?? localStorage.getItem('sim.phone') ?? '+15551234567',
  )
  const [name, setName] = useState(localStorage.getItem('sim.name') ?? 'Dana Customer')
  const [text, setText] = useState('')

  const send = useMutation({
    mutationFn: sendInbound,
    onSuccess: (res) => {
      setText('')
      qc.invalidateQueries()
      onStarted?.(res.conversation_id)
    },
  })

  const submit = () => {
    if (!text.trim() || !phone.trim()) return
    localStorage.setItem('sim.phone', phone)
    localStorage.setItem('sim.name', name)
    send.mutate({ phone: phone.trim(), name: name.trim(), text: text.trim() })
  }

  return (
    <FieldGroup>
      {!fixedPhone && (
        <div className="flex gap-3">
          <Field className="flex-1">
            <FieldLabel htmlFor="sim-phone">Phone</FieldLabel>
            <Input
              id="sim-phone"
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              className="font-mono"
            />
          </Field>
          <Field className="flex-1">
            <FieldLabel htmlFor="sim-name">Name</FieldLabel>
            <Input id="sim-name" value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        </div>
      )}
      <Field>
        <FieldLabel htmlFor="sim-text">Message</FieldLabel>
        <Textarea
          id="sim-text"
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit()
            }
          }}
          rows={3}
          placeholder="Type as the customer…"
          className="resize-none"
        />
      </Field>
      <div className="flex items-center justify-end gap-2">
        {send.isError && (
          <p className="text-xs text-destructive">{(send.error as Error).message}</p>
        )}
        <Button onClick={submit} disabled={send.isPending || !text.trim()}>
          {send.isPending && <Spinner data-icon="inline-start" />}
          Send as customer
        </Button>
      </div>
    </FieldGroup>
  )
}
