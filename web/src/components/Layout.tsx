import { Outlet } from '@tanstack/react-router'
import { AppSidebar } from '@/components/AppSidebar'

export function Layout(){return <div className="flex h-full"><AppSidebar/><main className="min-w-0 flex-1 overflow-hidden"><Outlet/></main></div>}
