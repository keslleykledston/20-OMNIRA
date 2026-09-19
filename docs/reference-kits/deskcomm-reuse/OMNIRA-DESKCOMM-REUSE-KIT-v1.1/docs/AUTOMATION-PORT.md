# Automation / Chatbot Port

## Fonte Deskcomm

Estudar:

```text
lib/automation/
lib/followup/
.specs/features/crm-automacao-fluxos/
docs/specs/07-spec-events-workers.md
```

## O que aproveitar

- graph schema;
- graph validation;
- condition semantics;
- action registry;
- anti-loop;
- run visibility;
- compile/decompile ideas;
- React Flow UX.

## O que não copiar

- event_log como runtime principal;
- cron-centric execution;
- Supabase access;
- Deskcomm-specific trigger/action names.

## OMNIRA model

```text
AutomationFlow
AutomationVersion
AutomationRun
AutomationStepRun
```

## Nodes MVP

```text
Message
Condition
CaptureInput
SetVariable
ToolCall
HumanHandoff
End
```

## Runtime

```text
domain event
 -> outbox
 -> NATS
 -> automation worker
 -> run state
 -> next step
```

## Versioning

Published version immutable.

Editing creates a draft/new version.

## Visual editor

Pode adaptar React Flow do Deskcomm se:
- desacoplado do data layer;
- convertido para Omnira UI;
- nodes alinhados ao schema OMNIRA.
