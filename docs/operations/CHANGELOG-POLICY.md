# Política de Changelog

Usar `CHANGELOG.md` para releases e `.changes/` para mudanças ainda não lançadas.

## Tipos

- `added`
- `changed`
- `fixed`
- `security`
- `deprecated`
- `removed`

## Arquivo de mudança

Exemplo: `.changes/20260918-hub-tenant-switch.md`

```md
type: added
scope: hub
breaking: false

Adiciona troca explícita de Tenant no Hub com revalidação de autorização.
```

## Regras

- escrever para humanos, não copiar mensagem de commit;
- descrever efeito observável;
- segurança pode omitir detalhes exploráveis;
- release agrega `.changes/`, atualiza versão e limpa entradas agregadas.
