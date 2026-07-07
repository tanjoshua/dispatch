import { useNavigate } from '@tanstack/react-router'
import { MessagesSquareIcon } from 'lucide-react'
import { Simulator } from '../components/Simulator'
import { Card, CardContent } from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'

export function Home() {
  const navigate = useNavigate()
  return (
    <div className="flex h-full items-center justify-center overflow-y-auto p-6">
      <div className="w-full max-w-md">
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <MessagesSquareIcon />
            </EmptyMedia>
            <EmptyTitle>Start a conversation</EmptyTitle>
            <EmptyDescription>
              Message the business as a customer would on WhatsApp. The intake agent
              picks it up, and every action it wants to take lands here for your review.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
        <Card className="mt-2">
          <CardContent>
            <Simulator
              onStarted={(conversationId) =>
                navigate({
                  to: '/conversations/$conversationId',
                  params: { conversationId },
                })
              }
            />
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
