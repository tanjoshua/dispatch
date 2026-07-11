import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { listChannels, sendInbound } from '../api'
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
	const channels = useQuery({ queryKey: ['channels'], queryFn: listChannels })
	const devConnections = (channels.data?.connections ?? []).filter(
		(connection) => connection.kind === 'dev' && connection.status === 'active',
	)
	const [connectionId, setConnectionId] = useState('')
	const selectedConnectionId = devConnections.some((connection) => connection.id === connectionId)
		? connectionId
		: (devConnections[0]?.id ?? '')
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
		if (!selectedConnectionId || !text.trim() || !phone.trim()) return
    localStorage.setItem('sim.phone', phone)
    localStorage.setItem('sim.name', name)
		send.mutate({ connectionId: selectedConnectionId, phone: phone.trim(), name: name.trim(), text: text.trim() })
  }

  return (
    <FieldGroup>
			<Field>
				<FieldLabel htmlFor="sim-connection">Development connection</FieldLabel>
				<select
					id="sim-connection"
					value={selectedConnectionId}
					disabled={channels.isLoading || devConnections.length === 0}
					onChange={(event) => setConnectionId(event.target.value)}
					className="h-8 w-full rounded-lg border border-input bg-transparent px-2.5 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
				>
					{devConnections.length === 0 && <option value="">No active development connection</option>}
					{devConnections.map((connection) => (
						<option key={connection.id} value={connection.id}>{connection.address}</option>
					))}
				</select>
			</Field>
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
				<Button onClick={submit} disabled={send.isPending || !selectedConnectionId || !text.trim()}>
          {send.isPending && <Spinner data-icon="inline-start" />}
          Send as customer
        </Button>
      </div>
    </FieldGroup>
  )
}
