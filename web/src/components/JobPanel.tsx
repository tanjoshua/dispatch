import type { Job, Run } from '../api'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardAction, CardHeader, CardTitle } from '@/components/ui/card'

const fields: Array<{ key: keyof Job; label: string }> = [
  { key: 'customer_name', label: 'Customer' },
  { key: 'phone', label: 'Phone' },
  { key: 'address', label: 'Address' },
  { key: 'issue', label: 'Issue' },
  { key: 'urgency', label: 'Urgency' },
]

// JobPanel shows the structured record the agent is building. Empty fields
// are visible on purpose: they're what intake still has to collect.
export function JobPanel({ job, run }: { job?: Job; run?: Run }) {
  return (
    <Card className="gap-0 py-0">
      <CardHeader className="border-b px-4 py-3">
        <CardTitle className="font-mono text-[11px] font-medium tracking-widest text-muted-foreground uppercase">
          Job record
        </CardTitle>
        <CardAction>
          {job?.status === 'intake_complete' ? (
            <Badge variant="ok" className="font-mono uppercase">
              intake complete
            </Badge>
          ) : run ? (
            <Badge variant="outline" className="font-mono text-muted-foreground uppercase">
              {run.status === 'running' ? 'intake in progress' : run.status}
            </Badge>
          ) : null}
        </CardAction>
      </CardHeader>
      <CardContent className="px-4 py-2">
        <dl>
          {fields.map(({ key, label }) => {
            const value = job?.[key] as string | undefined
            return (
              <div key={key} className="flex gap-2 border-b py-2 last:border-b-0">
                <dt className="w-20 shrink-0 pt-0.5 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                  {label}
                </dt>
                <dd
                  className={`m-0 min-w-0 text-sm ${value ? '' : 'text-muted-foreground/70 italic'}`}
                >
                  {value || 'not collected'}
                </dd>
              </div>
            )
          })}
        </dl>
      </CardContent>
    </Card>
  )
}
