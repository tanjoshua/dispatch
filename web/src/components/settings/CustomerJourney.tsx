import type { Pack } from '@/api'
import {
  BookOpenTextIcon,
  CheckIcon,
  GitBranchIcon,
  InboxIcon,
  MessageSquareTextIcon,
  ShieldCheckIcon,
  UserRoundCheckIcon,
  WrenchIcon,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { cn } from '@/lib/utils'

export type JourneyNode = 'entry' | 'triage' | 'inquiry' | 'service' | 'escalation' | 'quote'

function Step({
  icon: Icon,
  eyebrow,
  title,
  description,
  muted = false,
  selected = false,
  summary,
  onSelect,
}: {
  icon: typeof InboxIcon
  eyebrow?: string
  title: string
  description: string
  muted?: boolean
  selected?: boolean
  summary?: string
  onSelect?: () => void
}) {
  const content = (
    <div className="flex items-start gap-3 text-left">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-secondary">
        <Icon className="size-4" />
      </span>
      <div className="min-w-0">
        {eyebrow && <p className="font-mono text-[10px] font-medium tracking-wider text-muted-foreground uppercase">{eyebrow}</p>}
        <p className="text-sm font-semibold">{title}</p>
        <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{description}</p>
        {summary && <Badge className="mt-3" variant="secondary">{summary}</Badge>}
      </div>
    </div>
  )
  return onSelect ? (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={cn('relative w-full rounded-lg border bg-card p-4 transition-[border-color,box-shadow] hover:border-foreground/30 hover:shadow-sm focus-visible:outline-2', muted && 'border-dashed opacity-60', selected && 'border-foreground shadow-sm ring-2 ring-ring/20')}
    >
      {content}
    </button>
  ) : (
    <div className={cn('relative rounded-lg border bg-card p-4', muted && 'border-dashed opacity-60')}>
      {content}
    </div>
  )
}

function Arrow() {
  return <div aria-hidden className="flex h-7 items-center justify-center text-muted-foreground">↓</div>
}

const modelName=(id:string)=>id.split('-').map((part,index)=>index===0?part[0].toUpperCase()+part.slice(1):part==='opus'?'Opus':part==='sonnet'?'Sonnet':part).join(' ')
export function CustomerJourney({ pack, selected, onSelect, models, inquiryPolicy, servicePolicy }: { pack: Pack; selected: JourneyNode; onSelect: (node: JourneyNode) => void; models:Record<string,string>; inquiryPolicy: string; servicePolicy: string }) {
  const inquiry = pack.lanes.find((lane) => lane.id === 'inquiry')
  const service = pack.lanes.find((lane) => lane.id === 'service_job')
  const quote = pack.lanes.find((lane) => lane.id === 'quote_request')

  return (
    <Card>
      <CardHeader className="border-b">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle>Customer journey</CardTitle>
            <CardDescription>How the agent routes a customer message and moves the conversation forward.</CardDescription>
          </div>
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="secondary">Live</Badge>
            <Badge variant="outline">Coming soon</Badge>
          </div>
        </div>
      </CardHeader>
      <CardContent className="overflow-x-auto p-5 md:p-7">
        <div className="mx-auto min-w-[620px] max-w-4xl">
          <div className="mx-auto w-64">
            <Step selected={selected==='entry'} onSelect={()=>onSelect('entry')} icon={InboxIcon} eyebrow="Entry" title="Customer sends a message" description="A new message arrives from any connected channel." summary="All channels" />
            <Arrow />
            <Step selected={selected==='triage'} onSelect={()=>onSelect('triage')} icon={GitBranchIcon} eyebrow="Triage" title="Understand the intent" description="Decide whether this is a question, service work, or something requiring a person." summary={modelName(models.triage)} />
          </div>

          <div aria-hidden className="mx-auto h-8 w-2/3 rounded-t-xl border-x border-t" />

          <div className="grid grid-cols-3 gap-5">
            <section aria-label="Inquiry path" className="flex flex-col">
              <Step selected={selected==='inquiry'} onSelect={()=>onSelect('inquiry')} icon={BookOpenTextIcon} eyebrow="Inquiry" title={inquiry?.label ?? 'General inquiry'} description={inquiry?.description ?? 'Answer a general question.'} summary={`${modelName(models.inquiry)} · ${inquiryPolicy}`} />
              <Arrow />
              <Step icon={MessageSquareTextIcon} title="Answer from knowledge" description="Use only approved business facts, such as hours and service area." />
              <Arrow />
              <Step icon={ShieldCheckIcon} title="Review response" description="Send automatically or wait for dispatcher approval, based on policy." />
              <Arrow />
              <Step icon={CheckIcon} title="Reply and close" description="Deliver the answer without creating a service case." />
            </section>

            <section aria-label="Service request path" className="flex flex-col">
              <Step selected={selected==='service'} onSelect={()=>onSelect('service')} icon={WrenchIcon} eyebrow="Service work" title={service?.label ?? 'Service request'} description={service?.description ?? 'Collect details for requested work.'} summary={`${modelName(models.service_job)} · ${servicePolicy}`} />
              <Arrow />
              <Step icon={GitBranchIcon} title="New or existing case?" description="Match an existing case when unambiguous; otherwise create a new one." />
              <Arrow />
              <Step icon={MessageSquareTextIcon} title="Collect intake details" description="Gather the issue, address, urgency, and other required information." />
              <Arrow />
              <Step icon={UserRoundCheckIcon} title="Complete intake" description="Summarize the request and hand the structured case to the dispatcher." />
            </section>

            <section aria-label="Escalation and future paths" className="flex flex-col">
              <Step selected={selected==='escalation'} onSelect={()=>onSelect('escalation')} icon={UserRoundCheckIcon} eyebrow="Human help" title="Escalate to dispatcher" description="Flag ambiguity or unsupported requests and stand down for a person." summary="Always automatic" />
              <Arrow />
              <Step icon={ShieldCheckIcon} title="Dispatcher takes over" description="The agent stops responding while the dispatcher handles the conversation." />
              <div className="my-5 border-t" />
              <Step selected={selected==='quote'} onSelect={()=>onSelect('quote')} muted icon={WrenchIcon} eyebrow="Coming soon" title={quote?.label ?? 'Quote request'} description={quote?.description ?? 'A dedicated quoting path will live here.'} />
            </section>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
