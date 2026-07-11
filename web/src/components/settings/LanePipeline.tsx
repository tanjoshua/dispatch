import type { PackLane } from '@/api'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { cn } from '@/lib/utils'

export function LanePipeline({lane}:{lane:PackLane}) {
  return <Card size="sm" className={cn(lane.status!=='live'&&'border-dashed opacity-70')}>
    <CardHeader><CardTitle className="font-mono text-xs uppercase tracking-widest">{lane.label}</CardTitle><CardDescription>{lane.description}</CardDescription></CardHeader>
    <CardContent className="flex flex-wrap items-center gap-2">
      {lane.blocks.map((block,index)=><div className="flex items-center gap-2" key={block.id}>{index>0&&<span className="text-muted-foreground">→</span>}<Badge variant={block.status==='live'?'secondary':'outline'}>{block.label}{block.status!=='live'?' · Coming soon':''}</Badge></div>)}
    </CardContent>
  </Card>
}
