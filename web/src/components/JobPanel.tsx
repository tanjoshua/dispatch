import type { Case, Run } from '../api'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardAction, CardHeader, CardTitle } from '@/components/ui/card'

// The field-service "job record" view over a Case. Customer name and contact
// come from the customer/identity (not the case); address / issue / urgency are
// the field-service pack's fields in the case's `data` bag
// (design/004-domain-remodel.md §5). Empty fields are visible on purpose:
// they're what intake still has to collect.
type Field = { label: string; value?: string }

export function JobPanel({
  record,
  customerName,
  contact,
  run,
}: {
  record?: Case
  customerName?: string
  contact?: string
  run?: Run
}) {
  const data = record?.data ?? {}
  const fields: Field[] = [
    { label: 'Customer', value: customerName },
    { label: 'Phone', value: contact },
    { label: 'Address', value: data.address },
    { label: 'Issue', value: data.issue },
    { label: 'Urgency', value: data.urgency },
  ]
  return (
    <Card className="gap-0 py-0">
      <CardHeader className="border-b px-4 py-3">
        <CardTitle className="font-mono text-[11px] font-medium tracking-widest text-muted-foreground uppercase">
          Job record
        </CardTitle>
        <CardAction>
          {record?.status === 'intake_complete' ? (
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
          {fields.map(({ label, value }) => (
            <div key={label} className="flex gap-2 border-b py-2 last:border-b-0">
              <dt className="w-20 shrink-0 pt-0.5 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">
                {label}
              </dt>
              <dd
                className={`m-0 min-w-0 text-sm ${value ? '' : 'text-muted-foreground/70 italic'}`}
              >
                {value || 'not collected'}
              </dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  )
}
