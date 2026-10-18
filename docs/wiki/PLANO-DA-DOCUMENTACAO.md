# Plano da documentação e da wiki do OMNIRA

Estado: **versão 1 escrita em 2026-10-18** (esta pasta). Este plano diz para quem escrevemos, o que existe, o que falta e como manter tudo verdadeiro.

## 1. Para quem

| Público | O que precisa | Onde está |
|---|---|---|
| **Atendente** (opera as conversas) | Atender, transferir, finalizar, achar coisas rápido | Tutoriais 1 a 4, FAQ "Atendimento" |
| **Supervisor** | Filas, horários, SLA, quem atende o quê | Tutorial 5, FAQ "Administração" |
| **Administrador da empresa** | Pessoas, canais, setores, integrações, segurança | Tutoriais 5 e 6, FAQ "Administração" e "Segurança" |
| **Integrador / equipe do CRM** | Contrato da API, testar, ir para produção | Tutorial 7, pasta `integradores/`, FAQ "API" |
| **Equipe interna (engenharia/operação)** | Arquitetura, decisões, procedimentos | `docs/adr/`, `docs/ops/`, `docs/architecture/` (já existem; ver seção 4) |

## 2. Princípios

1. **Só o que existe.** Cada tela descrita foi capturada do sistema; cada comportamento citado foi conferido no código ou em teste. O que ainda não existe aparece como "ainda não" no FAQ, nunca como promessa.
2. **Tutorial = uma tarefa, com a tela.** Passos curtos, uma captura por passo importante, o que a pessoa vê ao terminar.
3. **Dados fictícios.** Nenhuma captura tem cliente, telefone ou empresa reais (ver seção 5).
4. **Uma fonte, vários formatos.** O texto vive em Markdown no repositório (`docs/wiki/`); a página publicada é gerada dele.
5. **Quem muda a tela muda a doc.** Mudança de interface que altera um tutorial refaz a captura (comando na seção 5) no mesmo trabalho.

## 3. Estrutura da wiki

```
docs/wiki/
  README.md                    início e mapa por perfil
  PLANO-DA-DOCUMENTACAO.md     este arquivo
  guias/01-visao-geral.md      a tela, o menu, os perfis
  tutoriais/01..07-*.md        tarefas passo a passo, com capturas
  integradores/                guia da API, roteiro de teste, contrato
  faq/FAQ.md                   perguntas frequentes por assunto
  GLOSSARIO.md                 termos
  imagens/                     capturas (geradas)
  _ferramentas/                dados fictícios e script de captura
  _site/                       gerador da página publicada
```

## 4. Inventário: o que cobre o quê

| Assunto | Estado | Onde |
|---|---|---|
| Atender uma conversa (assumir, responder, anexar, transferir, finalizar, linha do tempo) | **escrito** | Tutorial 1 |
| Busca rápida Ctrl+K | **escrito** | Tutorial 2 |
| Tickets e Pessoas: filtrar, ordenar, total | **escrito** | Tutorial 3 |
| Classificar contato, spam | **escrito** | Tutorial 4 |
| Filas, setores, horários | **escrito** | Tutorial 5 |
| Conectar um CRM (criar aplicação, token, suspender) | **escrito** | Tutorial 6 |
| Primeira chamada à API | **escrito** (comandos conferidos contra o contrato) | Tutorial 7 |
| Guia da API, roteiro de teste, contrato | **escrito** | `integradores/` |
| FAQ | **escrito** (Atendimento, Contatos e tickets, Administração, API, Segurança) | `faq/FAQ.md` |
| Glossário | **escrito** | `GLOSSARIO.md` |
| Conectar um número de WhatsApp (passo a passo do assistente) | **a escrever**: precisa de uma linha de teste para capturar o assistente real | próxima rodada |
| Pessoas, papéis e permissões | **a escrever** | próxima rodada |
| Automação (fluxos, menu de boas-vindas) | **a escrever** | próxima rodada |
| Supervisor: monitor de SLA e transbordo | **a escrever** | próxima rodada |
| Hub (atender várias empresas) | **a escrever**, público restrito | próxima rodada |
| Relatórios | não existe ainda no produto (tela oculta) | quando existir |
| Documentação técnica interna (ADRs, runbooks, deploy) | **existe**, fora desta wiki | `docs/adr`, `docs/ops`, `docs/architecture` |

## 5. Como manter (e regerar as capturas)

As capturas saem do aplicativo real, construído do código atual, com a API **simulada** e dados inventados (empresa "Empresa Exemplo", contatos "Ana Souza" etc.). Nada fala com o servidor de produção.

```bash
cd web && npx vite build && npx vite preview --host 127.0.0.1 --port 3190 --strictPort &
node docs/wiki/_ferramentas/capturar-telas.mjs            # todas as telas
node docs/wiki/_ferramentas/capturar-telas.mjs tickets    # só uma cena
python3 docs/wiki/_site/gerar.py                           # gera docs/wiki/_site/dist/ (a página)
```

Regras de revisão antes de publicar: conferir os passos contra a tela, rodar o `grep` de segredos (nada de token, e-mail ou telefone reais) e abrir a página gerada em largura de celular.

## 6. Publicação

- **Fonte:** Markdown no repositório (esta pasta). **Leitura:** página publicada (artefato privado, compartilhável por link) gerada de `docs/wiki/`.
- **Quem lê o quê:** a wiki completa é para clientes e integradores. O guia `PARA-O-DONO.md` e os documentos de operação **não** entram na página.
- **Versão:** cada página mostra a data da geração; o FAQ lista no fim o que mudou desde a versão anterior (preencher a cada rodada).
