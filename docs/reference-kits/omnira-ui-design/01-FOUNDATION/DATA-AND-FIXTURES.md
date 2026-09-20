# Dados, adapters e fixtures

## Regra

Componentes não importam fixtures diretamente. Estrutura recomendada:

```text
features/<feature>/
  components/
  hooks/
  data/
    repository.ts
    fixture-adapter.ts
    api-adapter.ts
  types/
```

## Fixtures visuais canônicas

Tenant: `K3G Solutions`
Usuário: `Alex Santos`
Contato principal: `Mariana Silva`
Telefone: `+55 11 98765-4321`
Ticket: `#1042`
Assunto: `Problema com pagamento`
Canal principal: WhatsApp

Outros nomes demo: Carlos Mendes, Ana Paula, João Ribeiro, Fernanda Lima, Lucas Souza, Juliana Alves, Pedro Henrique.

## Segurança

Nenhum fixture deve incluir token, api key ou credencial real. Provider secrets nunca entram em browser state.
