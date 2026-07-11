import type { ReactNode } from 'react'
export function PageShell({children}:{children:ReactNode}){return <div className="h-full overflow-y-auto p-5 lg:p-8"><div className="mx-auto max-w-6xl">{children}</div></div>}
