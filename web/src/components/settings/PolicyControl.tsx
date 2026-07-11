import type { PackTool, PolicyValue, ToolDecisionStats } from '@/api'
import { Field, FieldContent, FieldDescription, FieldTitle } from '@/components/ui/field'
import { Segmented } from '@/components/ui/segmented'

export function PolicyControl({tool,value,stats,lane,onChange}:{tool:PackTool;value:PolicyValue;stats?:ToolDecisionStats;lane:string;onChange:(value:PolicyValue)=>void}) {
  const decisions=stats ? `${stats.auto_approved} auto · ${stats.approved+stats.approved_with_edits} approved · ${stats.rejected} rejected` : 'No decisions yet'
  return <Field orientation="responsive">
    <FieldContent><FieldTitle>{tool.label}</FieldTitle><FieldDescription>{tool.risk}</FieldDescription><FieldDescription>{decisions} · all lanes</FieldDescription>{lane==='inquiry'&&tool.name==='propose_response'&&<FieldDescription>Attributed to inquiry only when no case is bound to the run.</FieldDescription>}</FieldContent>
    <Segmented value={value} allowed={tool.settings} onChange={onChange}/>
  </Field>
}
