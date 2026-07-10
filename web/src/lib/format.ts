// Formatting helpers for the review console: humans read relative times and
// field labels first; exact timestamps and raw payloads are one hover/click
// deeper.

export function timeAgo(iso: string): string {
  const seconds = Math.round((Date.now() - new Date(iso).getTime()) / 1000)
  if (seconds < 45) return 'just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

export function exactTime(iso: string): string {
  return new Date(iso).toLocaleString()
}

/**
 * Compact elapsed duration since `iso` ("42s", "3m", "2h", "1d") — how long a
 * pending action has been waiting. Distinct from timeAgo: a wait is a number
 * the dispatcher should feel, not a soft "just now".
 */
export function waitingFor(iso: string): string {
  const seconds = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

/** Seconds → compact duration ("42s", "3m", "2h") for latency stats. */
export function duration(seconds: number): string {
  if (seconds < 60) return `${Math.round(seconds)}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ${Math.round(seconds % 60)}s`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

export function prettyJson(v: unknown): string {
  if (v == null) return ''
  return typeof v === 'string' ? v : JSON.stringify(v, null, 2)
}

// Dispatcher-facing names for the agent's tools. Fall back to a generic
// prettified form for tools this map doesn't know yet.
const toolLabels: Record<string, string> = {
  send_message: 'Reply to customer',
  update_case: 'Update job record',
  continue_case: 'Continue previous job',
  close_case: 'Complete task',
  escalate: 'Escalate to dispatcher',
}

export function toolLabel(tool: string): string {
  return toolLabels[tool] ?? fieldLabel(tool)
}

/** snake_case / camelCase tool fields → human labels ("customer_name" → "Customer name"). */
export function fieldLabel(key: string): string {
  const words = key
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .replace(/[_-]+/g, ' ')
    .toLowerCase()
  return words.charAt(0).toUpperCase() + words.slice(1)
}

/**
 * Flatten a tool input into label/value rows for the human-readable summary.
 * Non-object payloads and nested values fall back to JSON text.
 */
export function summarize(input: unknown): Array<{ label: string; value: string }> | null {
  if (input == null || typeof input !== 'object' || Array.isArray(input)) return null
  const entries = Object.entries(input as Record<string, unknown>)
  if (entries.length === 0) return null
  return entries.map(([key, value]) => ({
    label: fieldLabel(key),
    value:
      typeof value === 'string'
        ? value
        : value == null
          ? '—'
          : JSON.stringify(value),
  }))
}
