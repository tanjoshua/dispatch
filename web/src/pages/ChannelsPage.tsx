import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleAlertIcon, PlusIcon, RadioIcon } from 'lucide-react'
import { createChannel, listChannels } from '@/api'
import { Alert, AlertAction, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'

export function ChannelsPage() {
  const queryClient = useQueryClient()
  const channels = useQuery({ queryKey: ['channels'], queryFn: listChannels })
  const [open, setOpen] = useState(false)
  const [address, setAddress] = useState(() => `test-${Date.now()}`)

  const create = useMutation({
		mutationFn: (intent: { commandId: string; address: string }) => createChannel({
			kind: 'dev', address: intent.address, commandId: intent.commandId,
		}),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['channels'] })
      setOpen(false)
      setAddress(`test-${Date.now()}`)
    },
  })

  const changeOpen = (next: boolean) => {
    setOpen(next)
    if (next) create.reset()
  }

  return (
    <section className="flex flex-col gap-6">
      <div className="flex flex-col items-start gap-3 sm:flex-row">
        <div>
          <h2 className="font-heading text-xl font-semibold">Channels</h2>
          <p className="text-sm text-muted-foreground">Connect customer entry points. Every connection uses this organization&apos;s Agent Behavior.</p>
        </div>

        <Sheet open={open} onOpenChange={changeOpen}>
          <SheetTrigger render={<Button className="sm:ml-auto" size="sm" />}>
            <PlusIcon data-icon="inline-start" />
            Add test connection
          </SheetTrigger>
          <SheetContent>
            <SheetHeader>
              <SheetTitle>Add test connection</SheetTitle>
              <SheetDescription>Create a development simulator endpoint. Agent Behavior is applied automatically.</SheetDescription>
            </SheetHeader>
            <div className="p-4">
              <FieldGroup>
                <Field data-invalid={create.isError}>
                  <FieldLabel htmlFor="channel-address">Address</FieldLabel>
                  <Input
                    id="channel-address"
                    value={address}
					disabled={create.isPending}
                    aria-invalid={create.isError}
                    onChange={(event) => {
                      setAddress(event.target.value)
                      create.reset()
                    }}
                  />
                </Field>
                {create.isError && (
                  <Alert variant="destructive">
                    <CircleAlertIcon />
                    <AlertTitle>Could not create connection</AlertTitle>
                    <AlertDescription>{create.error.message}</AlertDescription>
                  </Alert>
                )}
              </FieldGroup>
            </div>
            <SheetFooter>
				<Button disabled={!address.trim() || create.isPending} onClick={() => create.mutate({ commandId: crypto.randomUUID(), address: address.trim() })}>
                {create.isPending && <Spinner data-icon="inline-start" />}
                Create
              </Button>
            </SheetFooter>
          </SheetContent>
        </Sheet>
      </div>

      {channels.isLoading && (
        <div className="grid gap-3 md:grid-cols-2" aria-label="Loading channels">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      )}

      {channels.isError && (
        <Alert variant="destructive">
          <CircleAlertIcon />
          <AlertTitle>Could not load channels</AlertTitle>
          <AlertDescription>{channels.error.message}</AlertDescription>
          <AlertAction><Button variant="outline" size="sm" onClick={() => void channels.refetch()}>Try again</Button></AlertAction>
        </Alert>
      )}

      {channels.data && channels.data.connections.length === 0 && (
        <Empty className="min-h-48 border">
          <EmptyHeader>
            <EmptyMedia variant="icon"><RadioIcon /></EmptyMedia>
            <EmptyTitle>No channels connected</EmptyTitle>
            <EmptyDescription>Add a test connection to start a simulated customer conversation.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}

      {channels.data && channels.data.connections.length > 0 && (
        <div className="grid gap-3 md:grid-cols-2">
          {channels.data.connections.map((connection) => (
            <Card key={connection.id}>
              <CardHeader>
                <CardTitle>{connection.address}</CardTitle>
                <CardDescription>{connection.kind} · {connection.status}</CardDescription>
                <CardAction><Badge variant="secondary">v{connection.version}</Badge></CardAction>
              </CardHeader>
              <CardContent>
                <p className="text-sm text-muted-foreground">Uses the organization&apos;s Agent Behavior automatically.</p>
              </CardContent>
            </Card>
          ))}
        </div>
      )}

      {channels.data && channels.data.kinds.length > 0 && (
        <div className="flex flex-col gap-3">
          <h3 className="font-heading font-medium">Channel kinds</h3>
          <div className="grid gap-3 md:grid-cols-2">
            {channels.data.kinds.map((kind) => (
              <Card size="sm" key={kind.id}>
                <CardHeader>
                  <CardTitle>{kind.label}</CardTitle>
                  <CardAction><Badge variant={kind.status === 'available' ? 'secondary' : 'outline'}>{kind.status === 'available' ? 'Available' : 'Coming soon'}</Badge></CardAction>
                </CardHeader>
                <CardContent><p className="text-sm text-muted-foreground">{kind.description}</p></CardContent>
              </Card>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}
