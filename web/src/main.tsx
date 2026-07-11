import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Navigate, Outlet, createRootRoute, createRoute, createRouter, RouterProvider, useParams } from '@tanstack/react-router'
import './index.css'
import { Layout } from '@/components/Layout'
import { InboxLayout } from '@/components/InboxLayout'
import { PageShell } from '@/components/PageShell'
import { ConversationPage } from '@/pages/ConversationPage'
import { InboxEmptyState } from '@/pages/InboxEmptyState'
import { PlaybooksPage } from '@/pages/PlaybooksPage'
import { PlaybookDetailPage } from '@/pages/PlaybookDetailPage'
import { ChannelsPage } from '@/pages/ChannelsPage'
import { KnowledgePage } from '@/pages/KnowledgePage'
import { StatsPage } from '@/pages/StatsPage'
import { TooltipProvider } from '@/components/ui/tooltip'

const root=createRootRoute({component:Layout});const redirect=(to:string)=>()=> <Navigate to={to}/>
const index=createRoute({getParentRoute:()=>root,path:'/',component:redirect('/inbox')})
const inbox=createRoute({getParentRoute:()=>root,path:'/inbox',component:InboxLayout})
const inboxIndex=createRoute({getParentRoute:()=>inbox,path:'/',component:InboxEmptyState})
export const conversationRoute=createRoute({getParentRoute:()=>inbox,path:'/$conversationId',component:ConversationPage})
const LegacyConversation=()=>{const {conversationId}=useParams({strict:false});return <Navigate to="/inbox/$conversationId" params={{conversationId:String(conversationId)}}/>}
const legacyConversation=createRoute({getParentRoute:()=>root,path:'/conversations/$conversationId',component:LegacyConversation})
const shell=()=> <PageShell><Outlet/></PageShell>;const pages=createRoute({getParentRoute:()=>root,id:'pages',component:shell})
const playbooks=createRoute({getParentRoute:()=>pages,path:'/playbooks',component:PlaybooksPage});const playbook=createRoute({getParentRoute:()=>pages,path:'/playbooks/$playbookId',component:PlaybookDetailPage});const knowledge=createRoute({getParentRoute:()=>pages,path:'/knowledge',component:KnowledgePage});const channels=createRoute({getParentRoute:()=>pages,path:'/channels',component:ChannelsPage});const insights=createRoute({getParentRoute:()=>pages,path:'/insights',component:StatsPage})
const stats=createRoute({getParentRoute:()=>root,path:'/stats',component:redirect('/insights')});const settings=createRoute({getParentRoute:()=>root,path:'/settings',component:redirect('/playbooks')});const settingsPB=createRoute({getParentRoute:()=>root,path:'/settings/playbooks',component:redirect('/playbooks')});const settingsKnowledge=createRoute({getParentRoute:()=>root,path:'/settings/knowledge',component:redirect('/knowledge')});const settingsChannels=createRoute({getParentRoute:()=>root,path:'/settings/channels',component:redirect('/channels')})
const router=createRouter({routeTree:root.addChildren([index,inbox.addChildren([inboxIndex,conversationRoute]),legacyConversation,pages.addChildren([playbooks,playbook,knowledge,channels,insights]),stats,settings,settingsPB,settingsKnowledge,settingsChannels])})
declare module '@tanstack/react-router'{interface Register{router:typeof router}}
createRoot(document.getElementById('root')!).render(<StrictMode><QueryClientProvider client={new QueryClient()}><TooltipProvider><RouterProvider router={router}/></TooltipProvider></QueryClientProvider></StrictMode>)
