import type { AgentBehavior } from '@/api'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

type BehaviorField = keyof AgentBehavior
type BehaviorErrors = Partial<Record<BehaviorField, string>>

export function VoiceForm({
  behavior,
  disabled = false,
  errors,
  onChange,
}: {
  behavior: AgentBehavior
  disabled?: boolean
  errors: BehaviorErrors
  onChange: (behavior: AgentBehavior) => void
}) {
  const errorID = (field: BehaviorField) => `agent-behavior-${field}-error`

  return (
    <FieldGroup>
      <Field data-disabled={disabled} data-invalid={Boolean(errors.agent_name)}>
        <FieldLabel htmlFor="agent-name">Agent name</FieldLabel>
        <Input
          id="agent-name"
          value={behavior.agent_name}
				required
				maxLength={80}
          disabled={disabled}
          aria-invalid={Boolean(errors.agent_name)}
          aria-describedby={errors.agent_name ? errorID('agent_name') : undefined}
          onChange={(event) => onChange({ ...behavior, agent_name: event.target.value })}
        />
        <FieldDescription>The name used when the agent introduces itself.</FieldDescription>
        <FieldError id={errorID('agent_name')}>{errors.agent_name}</FieldError>
      </Field>

      <Field data-disabled={disabled} data-invalid={Boolean(errors.tone)}>
        <FieldLabel htmlFor="tone">Tone</FieldLabel>
        <Input
          id="tone"
          value={behavior.tone}
				required
				maxLength={240}
          disabled={disabled}
          aria-invalid={Boolean(errors.tone)}
          aria-describedby={errors.tone ? errorID('tone') : undefined}
          onChange={(event) => onChange({ ...behavior, tone: event.target.value })}
        />
        <FieldDescription>Plain-language guidance such as “warm, concise, and direct”.</FieldDescription>
        <FieldError id={errorID('tone')}>{errors.tone}</FieldError>
      </Field>

      <Field data-disabled={disabled} data-invalid={Boolean(errors.custom_instructions)}>
        <FieldLabel htmlFor="custom-instructions">Custom instructions</FieldLabel>
        <Textarea
          id="custom-instructions"
          value={behavior.custom_instructions}
				maxLength={4000}
          disabled={disabled}
          aria-invalid={Boolean(errors.custom_instructions)}
          aria-describedby={errors.custom_instructions ? errorID('custom_instructions') : undefined}
          placeholder="Optional organization-specific style guidance"
          onChange={(event) => onChange({ ...behavior, custom_instructions: event.target.value })}
        />
        <FieldDescription>Extra guidance for how the agent should communicate. Operational facts belong in Knowledge.</FieldDescription>
        <FieldError id={errorID('custom_instructions')}>{errors.custom_instructions}</FieldError>
      </Field>
    </FieldGroup>
  )
}
