# Processo de Release

1. CI verde.
2. E2E crítico verde.
3. suite de isolamento verde.
4. migrations revisadas.
5. findings P0/P1 resolvidos.
6. agregar `.changes/`.
7. atualizar `CHANGELOG.md`.
8. versionar.
9. deploy staging.
10. smoke test.
11. deploy production.
12. monitorar erros, filas, webhooks e adapters.
13. registrar incidente/rollback quando necessário.
