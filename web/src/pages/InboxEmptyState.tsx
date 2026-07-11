import { InboxIcon } from 'lucide-react'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
export function InboxEmptyState(){return <div className="grid h-full place-items-center"><Empty><EmptyHeader><EmptyMedia variant="icon"><InboxIcon/></EmptyMedia><EmptyTitle>Select a conversation</EmptyTitle><EmptyDescription>Choose a customer thread from the inbox to review or respond.</EmptyDescription></EmptyHeader></Empty></div>}
