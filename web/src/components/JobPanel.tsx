import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { correctCase, type Case, type Run } from '../api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardAction, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'

export function JobPanel({ record, candidates, conversationId, sourceMessageIds, customerName, contact, run }: {
  record?: Case
  candidates: Case[]
  conversationId: string
  sourceMessageIds: string[]
  customerName?: string
  contact?: string
  run?: Run
}) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [address, setAddress] = useState(record?.data.address ?? '')
  const [issue, setIssue] = useState(record?.data.issue ?? '')
  const [urgency, setUrgency] = useState(record?.data.urgency ?? '')
  const save = useMutation({mutationFn:()=>correctCase({conversationId,caseRecord:record!,patch:{address,issue,urgency},sourceMessageIds}),onSuccess:()=>{setEditing(false);qc.invalidateQueries()}})
  const fields = [['Customer', customerName], ['Phone', contact], ['Address', record?.data.address], ['Issue', record?.data.issue], ['Urgency', record?.data.urgency]]
  return <div className="flex flex-col gap-3">
    <Card className="gap-0 py-0">
      <CardHeader className="border-b px-4 py-3"><CardTitle className="font-mono text-[11px] font-medium tracking-widest text-muted-foreground uppercase">Selected case</CardTitle><CardAction>{record ? <Badge variant={record.status === 'intake_complete' ? 'ok' : 'outline'}>{record.status.replace('_',' ')}</Badge> : run ? <Badge variant="outline">unselected</Badge> : null}</CardAction></CardHeader>
      <CardContent className="flex flex-col gap-3 px-4 py-2">
        {editing && record ? <FieldGroup className="gap-3">
          <Field><FieldLabel htmlFor="case-address">Address</FieldLabel><Input id="case-address" value={address} onChange={e=>setAddress(e.target.value)}/></Field>
          <Field><FieldLabel htmlFor="case-issue">Issue</FieldLabel><Input id="case-issue" value={issue} onChange={e=>setIssue(e.target.value)}/></Field>
          <Field><FieldLabel htmlFor="case-urgency">Urgency</FieldLabel><Input id="case-urgency" value={urgency} onChange={e=>setUrgency(e.target.value)}/></Field>
          <div className="flex justify-end gap-2"><Button size="sm" variant="ghost" onClick={()=>setEditing(false)}>Cancel</Button><Button size="sm" disabled={save.isPending} onClick={()=>save.mutate()}>{save.isPending&&<Spinner data-icon="inline-start"/>}Save correction</Button></div>
        </FieldGroup> : <><dl>{fields.map(([label,value])=><div key={label} className="flex gap-2 border-b py-2 last:border-b-0"><dt className="w-20 shrink-0 pt-0.5 font-mono text-[10px] tracking-widest text-muted-foreground uppercase">{label}</dt><dd className="m-0 min-w-0 text-sm">{value||'not collected'}</dd></div>)}</dl>{record&&<Button size="sm" variant="outline" onClick={()=>setEditing(true)}>Correct case fields</Button>}</>}
        {save.isError&&<p className="text-xs text-destructive">{(save.error as Error).message}</p>}
      </CardContent>
    </Card>
    <Card className="gap-0 py-0"><CardHeader className="border-b px-4 py-3"><CardTitle className="font-mono text-[11px] tracking-widest text-muted-foreground uppercase">Customer cases</CardTitle></CardHeader><CardContent className="flex flex-col gap-2 px-4 py-3">{candidates.map(c=><div key={c.id} className="flex flex-col gap-1 rounded-md border p-2"><div className="flex items-center justify-between gap-2"><span className="truncate text-xs font-medium">{c.data.issue||'Issue not collected'}</span><Badge variant={c.id===record?.id?'default':'outline'}>{c.id===record?.id?'selected':c.status}</Badge></div><span className="truncate text-xs text-muted-foreground">{c.data.address||'Address not collected'} · v{c.version}</span></div>)}</CardContent></Card>
  </div>
}
