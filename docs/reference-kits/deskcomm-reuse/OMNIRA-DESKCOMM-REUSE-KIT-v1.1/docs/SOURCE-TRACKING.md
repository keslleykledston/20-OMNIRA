# Rastreamento de origem

Criar no OMNIRA:

```text
docs/research/deskcomm/SOURCE-MAP.md
```

Formato:

| OMNIRA | Deskcomm source | Commit | Class | Notes |
|---|---|---|---|---|
| internal/channels/meta/... | lib/channels/... | SHA | PORT | behavior only |
| apps/web/features/inbox/... | components/inbox/... | SHA | ADAPT | data layer replaced |

## Regra

Todo port deve ser fixado em commit.

Antes de iniciar uma wave:

```bash
git -C vendor/DeskcommCRM rev-parse HEAD
```

ou equivalente.

Preferir clone separado/read-only:

```text
references/DeskcommCRM/
```

Não adicionar como runtime dependency.

Não adicionar o `.git` do donor dentro do OMNIRA.
