import { LockIcon } from 'lucide-react'
import type { PolicyValue } from '@/api'
import { Badge } from '@/components/ui/badge'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

const options: {value:PolicyValue;label:string}[] = [
  {value:'auto',label:'Auto'}, {value:'require_review',label:'Review'}, {value:'forbid',label:'Forbid'},
]

export function Segmented({value,allowed,onChange}:{value:PolicyValue;allowed:PolicyValue[];onChange:(value:PolicyValue)=>void}) {
  if (allowed.length === 1) return <Badge variant="secondary"><LockIcon data-icon="inline-start" />{options.find(o=>o.value===allowed[0])?.label}</Badge>
  return <ToggleGroup value={[value]} onValueChange={(next)=>next[0]&&onChange(next[0] as PolicyValue)} variant="outline" size="sm" spacing={0} aria-label="Approval policy">
    {options.map(option => {
      const disabled=!allowed.includes(option.value)
      const item=<ToggleGroupItem key={option.value} value={option.value} disabled={disabled}>{disabled&&<LockIcon data-icon="inline-start" />}{option.label}</ToggleGroupItem>
      return disabled ? <Tooltip key={option.value}><TooltipTrigger render={<span />}>{item}</TooltipTrigger><TooltipContent>The pack floor does not allow this setting.</TooltipContent></Tooltip> : item
    })}
  </ToggleGroup>
}
