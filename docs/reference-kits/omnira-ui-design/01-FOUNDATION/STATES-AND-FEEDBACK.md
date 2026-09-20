# Estados e feedback

## Loading

Usar skeleton que preserve a geometria da tela. Spinner isolado somente em ações pequenas.

## Empty

Explicar o estado e dar CTA quando útil. Ex.: `Nenhum canal conectado` + `Adicionar canal`.

## Error

Mensagem curta, contextual, com retry quando possível. Não expor erro interno/provider token.

## Permission

Exibir estado de acesso insuficiente sem quebrar layout. O backend continua autoridade.

## Degraded

Para canal desconectado/degradado: banner contextual; histórico continua navegável; composer desabilita envio com motivo.

## Toasts

- sucesso: confirmação curta;
- erro: ação + motivo sanitizado;
- operações longas: progress/optimistic state somente se rollback seguro.
