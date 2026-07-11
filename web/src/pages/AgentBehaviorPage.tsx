import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BotIcon, CheckCircle2Icon, CircleAlertIcon, ShieldCheckIcon } from 'lucide-react'
import {
  ApiError,
  getAgentBehavior,
  type AgentBehavior,
  type AgentBehaviorResponse,
  updateAgentBehavior,
} from '@/api'
import { VoiceForm } from '@/components/settings/VoiceForm'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'

type BehaviorField = keyof AgentBehavior
type BehaviorErrors = Partial<Record<BehaviorField, string>>
type Notice = { kind: 'success' | 'conflict' | 'error'; message: string }
type SaveIntent = { behavior: AgentBehavior; expectedVersion: number; commandId: string }

const behaviorFields: BehaviorField[] = ['agent_name', 'tone', 'custom_instructions']

function copyBehavior(behavior: AgentBehavior): AgentBehavior {
  return { ...behavior }
}

function isDirty(base: AgentBehavior, draft: AgentBehavior) {
  return behaviorFields.some((field) => base[field] !== draft[field])
}

function validationErrors(body: unknown): BehaviorErrors {
  if (!body || typeof body !== 'object' || !('fields' in body)) return {}
  const fields = body.fields
  if (!fields || typeof fields !== 'object') return {}

  const result: BehaviorErrors = {}
  for (const field of behaviorFields) {
    const candidates = [field, `behavior.${field}`, `voice.${field}`]
    const message = candidates
      .map((key) => (fields as Record<string, unknown>)[key])
      .find((value): value is string => typeof value === 'string')
    if (message) result[field] = message
  }
  return result
}

function clientValidation(behavior: AgentBehavior): BehaviorErrors {
	const errors: BehaviorErrors = {}
	if (!behavior.agent_name.trim()) errors.agent_name = 'is required'
	else if (behavior.agent_name.length > 80) errors.agent_name = 'must be 80 characters or fewer'
	if (!behavior.tone.trim()) errors.tone = 'is required'
	else if (behavior.tone.length > 240) errors.tone = 'must be 240 characters or fewer'
	if (behavior.custom_instructions.length > 4000) errors.custom_instructions = 'must be 4000 characters or fewer'
	return errors
}

function LoadFailure({ error, retry }: { error: Error; retry: () => void }) {
  const missing = error instanceof ApiError && error.status === 404
  const unavailable = error instanceof ApiError && error.status === 503
  const title = missing
    ? 'Agent behavior is not configured'
    : unavailable
      ? 'Agent behavior is unavailable'
      : 'Could not load agent behavior'
  const description = missing
    ? 'This organization needs an Agent Behavior record before the agent can run.'
    : unavailable
      ? 'The current behavior configuration is invalid or temporarily unavailable.'
      : 'Check the server connection and try again.'

  return (
    <Empty className="min-h-72 border">
      <EmptyHeader>
        <EmptyMedia variant="icon"><CircleAlertIcon /></EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{description}</EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button variant="outline" size="sm" onClick={retry}>Try again</Button>
      </EmptyContent>
    </Empty>
  )
}

function LoadingState() {
  return (
    <section className="mx-auto flex max-w-3xl flex-col gap-5" aria-label="Loading agent behavior">
      <div className="flex flex-col gap-2">
        <Skeleton className="h-7 w-48" />
        <Skeleton className="h-4 w-full max-w-xl" />
      </div>
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-96 w-full" />
    </section>
  )
}

export function AgentBehaviorPage() {
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: ['agent-behavior'], queryFn: getAgentBehavior, staleTime: Infinity })
  const [base, setBase] = useState<AgentBehavior>()
  const [draft, setDraft] = useState<AgentBehavior>()
  const [version, setVersion] = useState<number>()
  const [errors, setErrors] = useState<BehaviorErrors>({})
  const [notice, setNotice] = useState<Notice>()

  const applyResponse = (response: AgentBehaviorResponse) => {
    setBase(copyBehavior(response.behavior))
    setDraft(copyBehavior(response.behavior))
    setVersion(response.version)
  }

  useEffect(() => {
    if (query.data?.behavior && base === undefined) applyResponse(query.data)
  }, [base, query.data])

  const save = useMutation({
    mutationFn: (intent: SaveIntent) => updateAgentBehavior({
      behavior: intent.behavior,
      expectedVersion: intent.expectedVersion,
      commandId: intent.commandId,
    }),
    retry: (failureCount, error) => !(error instanceof ApiError) && failureCount < 1,
    retryDelay: 500,
    onSuccess: (response) => {
      queryClient.setQueryData(['agent-behavior'], response)
      applyResponse(response)
      setErrors({})
      setNotice({ kind: 'success', message: 'Agent behavior saved. Changes apply on the next agent turn.' })
    },
    onError: async (error) => {
      if (error instanceof ApiError && error.status === 409) {
        setErrors({})
        try {
          const latest = await getAgentBehavior()
          queryClient.setQueryData(['agent-behavior'], latest)
          applyResponse(latest)
          setNotice({ kind: 'conflict', message: 'Your edit was not saved because these settings changed elsewhere. The latest settings are now loaded.' })
        } catch {
          setNotice({ kind: 'error', message: 'Your edit was not saved, and the latest settings could not be loaded. Try again.' })
        }
        return
      }
      if (error instanceof ApiError && error.status === 422) {
        const nextErrors = validationErrors(error.body)
        setErrors(nextErrors)
        setNotice(Object.keys(nextErrors).length === 0
          ? { kind: 'error', message: error.message }
          : undefined)
        return
      }
      setNotice({ kind: 'error', message: error.message })
    },
  })

  if (query.isLoading) return <LoadingState />
  if (query.isError) return <LoadFailure error={query.error} retry={() => void query.refetch()} />
  if (!query.data?.behavior) {
    return <LoadFailure error={new Error('Agent behavior response was incomplete')} retry={() => void query.refetch()} />
  }
  if (!base || !draft || version === undefined) {
    return <LoadingState />
  }

  const dirty = isDirty(base, draft)
  const updateDraft = (behavior: AgentBehavior) => {
    setDraft(behavior)
    setErrors({})
    setNotice(undefined)
  }
	const submit = () => {
		const nextErrors = clientValidation(draft)
		if (Object.keys(nextErrors).length > 0) {
			setErrors(nextErrors)
			setNotice(undefined)
			return
		}
		save.mutate({
			behavior: copyBehavior(draft),
			expectedVersion: version,
			commandId: crypto.randomUUID(),
		})
	}

  return (
    <section className="mx-auto flex max-w-3xl flex-col gap-5">
      <header className="flex flex-col gap-2">
        <div className="flex items-center gap-2 text-muted-foreground">
          <BotIcon />
          <span className="font-mono text-xs tracking-widest uppercase">Organization settings</span>
        </div>
        <h2 className="font-heading text-2xl font-semibold">Agent Behavior</h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Set how the agent presents itself across every customer conversation for this organization.
        </p>
      </header>

      <Alert>
        <ShieldCheckIcon />
        <AlertTitle>Customer replies and case changes require review</AlertTitle>
        <AlertDescription>
          A dispatcher approves all customer communication and business mutations. Read-only lookups and narrow routing or safety controls can run automatically.
        </AlertDescription>
      </Alert>

      <Card>
        <CardHeader>
          <CardTitle>Voice and guidance</CardTitle>
          <CardDescription>
            These settings shape every lane. Models, tools, and approval policy are managed by Dispatch.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <VoiceForm behavior={draft} disabled={save.isPending} errors={errors} onChange={updateDraft} />
        </CardContent>
        <CardFooter className="flex-col items-start gap-3 sm:flex-row sm:items-center">
          <Button disabled={!dirty || save.isPending} onClick={submit}>
            {save.isPending && <Spinner data-icon="inline-start" />}
            Save behavior
          </Button>
          <p className="text-xs text-muted-foreground sm:ml-auto">Version {version} · Changes apply on the next agent turn.</p>
        </CardFooter>
      </Card>

      {notice && (
        <Alert variant={notice.kind === 'error' ? 'destructive' : 'default'} aria-live="polite">
          {notice.kind === 'success' ? <CheckCircle2Icon /> : <CircleAlertIcon />}
          <AlertTitle>{notice.kind === 'success' ? 'Saved' : notice.kind === 'conflict' ? 'Settings reloaded' : 'Could not save'}</AlertTitle>
          <AlertDescription>{notice.message}</AlertDescription>
        </Alert>
      )}
    </section>
  )
}
