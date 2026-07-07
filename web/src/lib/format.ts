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

export function prettyJson(v: unknown): string {
  if (v == null) return ''
  return typeof v === 'string' ? v : JSON.stringify(v, null, 2)
}

// Dispatcher-facing names for the agent's tools. Fall back to a generic
// prettified form for tools this map doesn't know yet.
const toolLabels: Record<string, string> = {
  send_message: 'Reply to customer',
  update_job: 'Update job record',
  close_job: 'Complete intake',
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
