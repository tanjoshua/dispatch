import { useQuery } from '@tanstack/react-query'
import { getDecisionStats } from '../api'
import { Badge } from '@/components/ui/badge'
import { duration, toolLabel, waitingFor } from '@/lib/format'

// Decision stats: per-tool outcomes and human-decision latency. This is the
// evidence for evaluating the fixed review policy. Policy is not configurable
// by an organization; any future autonomy change is a product rollout.
export function StatsPage() {
  const { data } = useQuery({
    queryKey: ['decision-stats'],
    queryFn: getDecisionStats,
    refetchInterval: 5000,
  })
  const tools = data?.tools ?? []

  return (
    <div className="h-full overflow-y-auto p-6">
      <h1 className="font-heading text-sm font-bold tracking-[0.2em] uppercase">
        Decision stats
      </h1>
      <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
        How the review queue is behaving, per tool: what humans approve, edit,
        or reject, and how long decisions wait. Approval policy is fixed today;
        these results inform any future product-level autonomy rollout.
      </p>

      {tools.length === 0 ? (
        <p className="mt-6 text-sm text-muted-foreground">No actions yet.</p>
      ) : (
        <div className="mt-5 overflow-x-auto">
          <table className="w-full min-w-[720px] border-collapse text-sm">
            <thead>
              <tr className="border-b text-left font-mono text-[11px] tracking-widest text-muted-foreground uppercase">
                <th className="py-2 pr-4 font-medium">Tool</th>
                <th className="py-2 pr-4 font-medium">Proposed</th>
                <th className="py-2 pr-4 font-medium">Auto</th>
                <th className="py-2 pr-4 font-medium">Approved</th>
                <th className="py-2 pr-4 font-medium">Edited</th>
                <th className="py-2 pr-4 font-medium">Rejected</th>
                <th className="py-2 pr-4 font-medium">Stood down</th>
                <th className="py-2 pr-4 font-medium">Pending</th>
                <th className="py-2 pr-4 font-medium">Median wait</th>
                <th className="py-2 font-medium">Avg wait</th>
              </tr>
            </thead>
            <tbody>
              {tools.map((t) => {
                const humanDecided =
                  t.approved + t.approved_with_edits + t.rejected
                const pct = (n: number) =>
                  humanDecided > 0 ? ` (${Math.round((n / humanDecided) * 100)}%)` : ''
                return (
                  <tr key={t.tool} className="border-b align-baseline">
                    <td className="py-2.5 pr-4">
                      <span className="font-medium">{toolLabel(t.tool)}</span>
                      <span className="ml-2 font-mono text-[11px] text-muted-foreground">
                        {t.tool}
                      </span>
                    </td>
                    <td className="py-2.5 pr-4 font-mono">{t.proposed}</td>
                    <td className="py-2.5 pr-4 font-mono">{t.auto_approved}</td>
                    <td className="py-2.5 pr-4 font-mono">
                      {t.approved}
                      <span className="text-muted-foreground">{pct(t.approved)}</span>
                    </td>
                    <td className="py-2.5 pr-4 font-mono">
                      {t.approved_with_edits}
                      <span className="text-muted-foreground">{pct(t.approved_with_edits)}</span>
                    </td>
                    <td className="py-2.5 pr-4 font-mono">
                      {t.rejected}
                      <span className="text-muted-foreground">{pct(t.rejected)}</span>
                    </td>
                    {/* dismiss + supersede: the human handled it another way */}
                    <td className="py-2.5 pr-4 font-mono">{t.dismissed + t.superseded}</td>
                    <td className="py-2.5 pr-4">
                      {t.pending > 0 ? (
                        <Badge variant="signal" className="font-mono">
                          {t.pending}
                          {t.oldest_pending_at
                            ? ` · ${waitingFor(t.oldest_pending_at)}`
                            : ''}
                        </Badge>
                      ) : (
                        <span className="font-mono text-muted-foreground">0</span>
                      )}
                    </td>
                    <td className="py-2.5 pr-4 font-mono">
                      {t.median_decision_seconds != null
                        ? duration(t.median_decision_seconds)
                        : '—'}
                    </td>
                    <td className="py-2.5 font-mono">
                      {t.avg_decision_seconds != null
                        ? duration(t.avg_decision_seconds)
                        : '—'}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
