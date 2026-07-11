import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { BarChart3Icon, BookOpenIcon, InboxIcon, LibraryIcon, MessageSquareMoreIcon, RadioIcon } from 'lucide-react'
import { listChannels, listConversations } from '@/api'
import { Simulator } from '@/components/Simulator'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { waitingFor } from '@/lib/format'
import { cn } from '@/lib/utils'

const groups=[{label:'Operate',items:[['Inbox','/inbox',InboxIcon]]},{label:'Configure',items:[['Playbooks','/playbooks',BookOpenIcon],['Knowledge','/knowledge',LibraryIcon],['Channels','/channels',RadioIcon],['Insights','/insights',BarChart3Icon]]}] as const
export function AppSidebar(){
 const [simulate,setSimulate]=useState(false);const navigate=useNavigate();
 const {data}=useQuery({queryKey:['conversations'],queryFn:listConversations,refetchInterval:3000});const channels=useQuery({queryKey:['channels'],queryFn:listChannels})
 const conversations=data?.conversations??[];const pending=conversations.reduce((n,c)=>n+c.pending_count,0);const oldest=conversations.flatMap(c=>c.oldest_pending_at?[c.oldest_pending_at]:[]).sort()[0];const hasDev=channels.data?.connections.some(c=>c.kind==='dev'&&c.status==='active')
 return <aside className="flex w-14 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground lg:w-56"><Link to="/inbox" className="flex h-14 items-center gap-3 border-b px-4"><span className="size-2.5 rounded-[2px] bg-sidebar-primary"/><span className="hidden font-heading text-sm font-bold tracking-[0.2em] uppercase lg:block">Dispatch</span></Link><nav className="flex flex-1 flex-col gap-6 py-4">{groups.map(group=><div key={group.label} className="flex flex-col gap-1"><p className="hidden px-4 font-mono text-[10px] tracking-widest text-muted-foreground uppercase lg:block">{group.label}</p>{group.items.map(([label,to,Icon])=><Tooltip key={to}><TooltipTrigger render={<Link to={to} activeOptions={{exact:to==='/inbox'}} className="relative flex h-10 items-center gap-3 px-4 text-sm hover:bg-sidebar-accent" activeProps={{className:'bg-sidebar-accent before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-sidebar-primary'}}/>}><Icon/><span className="hidden lg:block">{label}</span>{to==='/inbox'&&pending>0&&<Badge variant="signal" className={cn('ml-auto hidden font-mono lg:flex','pulse-soft')}>{pending}{oldest?` · ${waitingFor(oldest)}`:''}</Badge>}</TooltipTrigger><TooltipContent side="right" className="lg:hidden">{label}</TooltipContent></Tooltip>)}</div>)}</nav>{hasDev&&<div className="border-t p-2"><Tooltip><TooltipTrigger render={<Button variant="ghost" className="w-full justify-start" onClick={()=>setSimulate(true)}/> }><MessageSquareMoreIcon/><span className="hidden lg:block">Simulate customer</span></TooltipTrigger><TooltipContent side="right" className="lg:hidden">Simulate customer</TooltipContent></Tooltip></div>}<Sheet open={simulate} onOpenChange={setSimulate}><SheetContent><SheetHeader><SheetTitle>Simulate customer</SheetTitle><SheetDescription>Send a message through the development channel.</SheetDescription></SheetHeader><div className="p-4"><Simulator onStarted={id=>{setSimulate(false);navigate({to:'/inbox/$conversationId',params:{conversationId:id}})}}/></div></SheetContent></Sheet></aside>
}
