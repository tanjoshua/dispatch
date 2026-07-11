import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { ChevronLeftIcon } from 'lucide-react'
import { ApiError, getDecisionStats, getPacks, getPlaybook, type PlaybookConfig, type PolicyValue, updatePlaybookConfig } from '@/api'
import { CustomerJourney, type JourneyNode } from '@/components/settings/CustomerJourney'
import { PolicyControl } from '@/components/settings/PolicyControl'
import { VoiceForm } from '@/components/settings/VoiceForm'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { FieldError, FieldGroup } from '@/components/ui/field'
import { Separator } from '@/components/ui/separator'
import { Spinner } from '@/components/ui/spinner'

const titles: Record<JourneyNode, string> = {
  entry: 'Message entry', triage: 'Understand intent', inquiry: 'Inquiry path',
  service: 'Service path', escalation: 'Human escalation', quote: 'Quote request',
}

export function PlaybookDetailPage(){
  const {playbookId=''}=useParams({strict:false})
  const client=useQueryClient();const playbookQuery=useQuery({queryKey:['playbook',playbookId],queryFn:()=>getPlaybook(playbookId)});const packsQuery=useQuery({queryKey:['packs'],queryFn:getPacks});const statsQuery=useQuery({queryKey:['decision-stats'],queryFn:getDecisionStats})
  const [draft,setDraft]=useState<PlaybookConfig>();const [selectedNode,setSelectedNode]=useState<JourneyNode>('triage');const [notice,setNotice]=useState('');const [errors,setErrors]=useState<Record<string,string>>({})
  useEffect(()=>{if(playbookQuery.data)setDraft(structuredClone(playbookQuery.data.effective.config))},[playbookQuery.data])
  const pack=useMemo(()=>packsQuery.data?.packs.find(p=>p.id===draft?.pack),[packsQuery.data,draft?.pack]);const dirty=!!draft&&!!playbookQuery.data&&JSON.stringify(draft)!==JSON.stringify(playbookQuery.data.playbook.config)
  const save=useMutation({mutationFn:()=>updatePlaybookConfig(playbookQuery.data!.playbook,draft!),onSuccess:data=>{client.setQueryData(['playbook',playbookId],data);setDraft(structuredClone(data.playbook.config));setNotice('Saved. Changes apply on the next agent turn.');setErrors({})},onError:async(error)=>{if(error instanceof ApiError&&error.status===409){setNotice('This playbook changed elsewhere. The latest version was reloaded.');await playbookQuery.refetch();return}if(error instanceof ApiError&&error.status===422){const body=error.body as {fields?:Record<string,string>};setErrors(body.fields??{});return}setNotice(error.message)}})
  if(!draft||!pack||!playbookQuery.data)return <Spinner/>
  const stats=statsQuery.data?.tools??[]
  const policyLabel=(lane:string,tool:string)=>({auto:'Automatic',require_review:'Requires review',forbid:'Forbidden'}[draft.policy[lane]?.[tool]??'']??'Pack default')
  const laneTools=(lane:string)=>pack.lanes.find(item=>item.id===lane)?.tools??[]
  const setPolicy=(lane:string,tool:string,value:PolicyValue)=>setDraft({...draft,policy:{...draft.policy,[lane]:{...draft.policy[lane],[tool]:value}}})
  const policies=(lane:string,names?:string[])=><FieldGroup>{laneTools(lane).filter(tool=>!names||names.includes(tool.name)).map(tool=><PolicyControl key={tool.name} lane={lane} tool={tool} value={draft.policy[lane]?.[tool.name]??tool.default} stats={stats.find(s=>s.tool===tool.name)} onChange={value=>setPolicy(lane,tool.name,value)}/>)}</FieldGroup>

  return <section className="flex flex-col gap-6">
    <div className="flex flex-col gap-3"><Link to="/playbooks" className="flex w-fit items-center gap-1 text-sm text-muted-foreground hover:text-foreground"><ChevronLeftIcon className="size-4"/>Playbooks</Link><div><h2 className="font-heading text-xl font-semibold">{playbookQuery.data.playbook.name}</h2><p className="text-sm text-muted-foreground">Select a step to configure how it behaves. Dispatch tunes the model for every journey stage.</p></div></div>
    <div className="grid items-start gap-5 xl:grid-cols-[minmax(0,1fr)_22rem]">
      <CustomerJourney pack={pack} selected={selectedNode} onSelect={setSelectedNode} models={playbookQuery.data.effective.models} inquiryPolicy={policyLabel('inquiry','propose_response')} servicePolicy={policyLabel('service_job','propose_response')}/>
      <Card className="xl:sticky xl:top-0"><CardHeader><CardTitle>{titles[selectedNode]}</CardTitle><CardDescription>Settings for the selected step.</CardDescription></CardHeader><CardContent className="flex flex-col gap-5">
        {selectedNode==='entry'&&<><p className="text-sm text-muted-foreground">This playbook receives messages from every channel routed to it.</p><Link to="/channels"><Button variant="outline" size="sm">Configure channels</Button></Link></>}
        {selectedNode==='triage'&&<p className="text-sm text-muted-foreground">Dispatch configures and continuously tunes the model for this stage. The model shown in the journey is the one currently used.</p>}
        {selectedNode==='inquiry'&&<>{policies('inquiry')}<Separator/><VoiceForm voice={draft.voice} onChange={voice=>setDraft({...draft,voice})}/></>}
        {selectedNode==='service'&&policies('service_job',['propose_response','create_case','select_case','update_case','list_candidate_cases'])}
        {selectedNode==='escalation'&&policies('service_job',['escalate','stand_down','wait_for_external'])}
        {selectedNode==='quote'&&<p className="text-sm text-muted-foreground">This path is not available yet. Its configuration will appear here when quote execution is implemented.</p>}
      </CardContent></Card>
    </div>
    {Object.entries(errors).map(([field,message])=><FieldError key={field}>{field}: {message}</FieldError>)}<Separator/><div className="sticky bottom-0 flex flex-wrap items-center gap-3 border-t bg-background py-3"><Button disabled={!dirty||save.isPending} onClick={()=>save.mutate()}>{save.isPending&&<Spinner data-icon="inline-start"/>}Save changes</Button>{notice&&<p className="text-sm text-muted-foreground">{notice}</p>}<p className="ml-auto max-w-md text-xs text-muted-foreground">Changes apply from the agent&apos;s next turn.</p></div>
  </section>
}
