# Evidência de review visual

`docs/reference-kits/omnira-ui-design/references/` é a **referência canônica** de design.
Os arquivos aqui são **evidência de implementação**, não source of truth.

Política:

- versionar apenas o screenshot **final aprovado** de cada wave, e somente quando servir
  de baseline de regressão (na prática: o viewport de gate, desktop 1440×1024);
- capturas intermediárias, reprovadas ou de sanity responsive não vão para o Git;
- capturar sempre contra build de produção (`npm run build` + `vite preview`), nunca
  contra um dev server antigo — Tailwind não recarrega mudanças de config em servidor
  já em execução e o CSS servido fica obsoleto.
