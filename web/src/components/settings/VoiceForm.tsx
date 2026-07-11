import type { VoiceConfig } from '@/api'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

export function VoiceForm({voice,onChange}:{voice:VoiceConfig;onChange:(voice:VoiceConfig)=>void}){
  return <FieldGroup><Field><FieldLabel htmlFor="agent-name">Agent name</FieldLabel><Input id="agent-name" value={voice.agent_name} onChange={e=>onChange({...voice,agent_name:e.target.value})}/></Field><Field><FieldLabel htmlFor="tone">Tone</FieldLabel><Input id="tone" value={voice.tone} onChange={e=>onChange({...voice,tone:e.target.value})}/><FieldDescription>Plain-language guidance such as “warm, concise, and direct”.</FieldDescription></Field><Field><FieldLabel htmlFor="custom-instructions">Custom instructions</FieldLabel><Textarea id="custom-instructions" value={voice.custom_instructions} onChange={e=>onChange({...voice,custom_instructions:e.target.value})} placeholder="Optional organization-specific style guidance"/></Field></FieldGroup>
}
