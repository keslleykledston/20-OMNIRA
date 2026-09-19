# Como extrair e usar

## 1. Extraia o ZIP

Linux/macOS:

```bash
unzip OMNIRA-DESKCOMM-REUSE-KIT-v1.0.zip
```

Windows PowerShell:

```powershell
Expand-Archive .\OMNIRA-DESKCOMM-REUSE-KIT-v1.0.zip
```

## 2. Copie para o projeto OMNIRA

Sugestão:

```text
docs/reference-kits/deskcomm-reuse/
```

ou entregue a pasta inteira ao agente.

Não sobrescreva automaticamente `START-HERE.md` do OMNIRA.
Este kit é complementar.

## 3. Envie ao Claude/Codex

Mensagem:

```text
Leia integralmente o pacote OMNIRA-DESKCOMM-REUSE-KIT-v1.0.
Em seguida execute PROMPT-CLAUDE-CODE.txt.
O DeskcommCRM é donor/reference, não base arquitetural.
Comece apenas pela FASE 1 — AUDIT.
```

## 4. Não copie o Deskcomm dentro do runtime

Se o agente precisar clonar o donor:

```text
references/DeskcommCRM/
```

ou fora do repositório.

Não importar seus pacotes como dependência runtime.

## 5. Primeira entrega esperada

Sem código de feature.

Primeiro:

```text
docs/research/deskcomm/REUSE-AUDIT.md
docs/research/deskcomm/SOURCE-MAP.md
THIRD_PARTY_NOTICES.md (pode nascer vazio/template até primeira cópia)
```

Depois iniciar Wave D1.
