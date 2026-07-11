import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { ArrowRightIcon } from 'lucide-react'
import { listPlaybooks } from '@/api'
import { Button } from '@/components/ui/button'
import { Card, CardAction, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'

export function PlaybooksPage(){const {data,isLoading}=useQuery({queryKey:['playbooks'],queryFn:listPlaybooks});return <section className="flex flex-col gap-4"><div><h2 className="font-heading text-xl font-semibold">Playbooks</h2><p className="text-sm text-muted-foreground">Configure how the agent handles each lane. Routing itself stays pack-owned.</p></div>{isLoading&&<Skeleton className="h-28 w-full"/>}{data?.playbooks.map(playbook=><Card key={playbook.id}><CardHeader><CardTitle>{playbook.name}</CardTitle><CardDescription>{playbook.agent} · version {playbook.version}</CardDescription><CardAction><Link to="/playbooks/$playbookId" params={{playbookId:playbook.id}}><Button variant="outline" size="sm">Configure<ArrowRightIcon data-icon="inline-end"/></Button></Link></CardAction></CardHeader></Card>)}</section>}
