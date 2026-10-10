# Roteiro de teste ao vivo do Hub (login real, estado LAB)

Quem executa: o dono, no navegador, com o login real do Keycloak. Nada aqui é provado pelos testes automatizados: o E2E usa
sessões fabricadas. Marque cada passo como OK / FALHOU e anote a tela. Não use dados de cliente real.

Pré-requisitos: uma conta de administrador do Hub e uma segunda conta (agente) com e-mail que você controle; a empresa de teste
(instância) já existente; um celular de teste para o WhatsApp (número que não seja de cliente).

## A. Painel de acessos (`/acessos`)
1. Entrar como administrador do Hub. O menu lateral NÃO tem "Hub"; tem "Acessos". Abrir `/acessos`.
2. As abas aparecem: Agentes, Instâncias, Equipes, Auditoria. Cada uma carrega sem erro.
3. `/hub` redireciona para Conversas e `/hub/empresas` para a aba Instâncias de `/acessos`.

## B. Autorizar uma pessoa por e-mail
4. Em Agentes, autorizar o e-mail do agente de teste para uma instância (permissão de resposta).
5. Entrar com a conta do agente: ela vê só a(s) instância(s) autorizada(s).
6. Tentar autorizar o mesmo e-mail em OUTRA instância como operador: deve recusar com a mensagem "já atua em outra instância".

## C. Convite de equipe (corrigido em 2026-10-09 — este é o passo mais importante)
7. Na instância, Pessoas > Equipe (`/settings/people/team`) > convidar um e-mail novo. Abrir o link `/invite/<token>` logado com SSO nesse e-mail.
8. Clicar "Aceitar convite": deve mostrar "Convite aceito" e entrar. (Antes da correção dava erro.)
9. Abrir o mesmo link de novo: "já aceito". Convite revogado: mensagem de revogado.

## D. Conversas unificadas
10. Em Conversas, o seletor de instâncias lista só as suas; a origem de cada conversa mostra o logo do canal.
11. Assumir uma conversa, responder, transferir para outro agente, devolver à fila. Conferir o histórico.

## E. Delegação de canais
12. Instâncias > delegar "Canais" e "Integrações" para a instância. Em Agentes, marcar "Gerenciar" para o agente de teste.
13. Entrar como esse agente: aparece "Canais das instâncias" no menu. Criar uma conexão de CRM pela tela.
14. Sem a chave "Gerenciar", o mesmo agente NÃO vê o item e a API recusa.

## F. WhatsApp real pelo Hub (ainda nunca feito)
15. Pelo Hub, criar o canal WhatsApp da instância e parear o número de teste (QR).
16. Mandar uma mensagem de outro celular: ela aparece em Conversas com o logo do WhatsApp; responder e conferir entrega/leitura.

## G. Equipe em rodízio (a distribuição nunca rodou em produção)
17. Em Equipes, criar uma equipe em modo Automática com 2 agentes e capacidade 1 cada; ligar a(s) instância(s).
18. Mandar 2 mensagens novas: cada agente recebe uma. A terceira fica na fila. Esperar até ~15 s (intervalo do distribuidor).
19. Revogar o acesso de um agente: ele deixa de receber novas conversas.

## H. Auditoria e suspensão
20. Aba Auditoria: aparecem as mudanças acima (acesso, delegação, equipe) e NENHUMA mensagem ou atendimento.
21. Suspender a instância de teste: o agente perde acesso; reativar restaura.

Resultado esperado de qualquer FALHOU: me enviar o passo, a tela e a hora aproximada; os logs do servidor são consultados por mim.

## I. Classificar o contato pelo Hub (fase 04a; precisa do preset `classificacao`)

Pré-requisito: `serving ceiling ... --preset classificacao` e `serving grant ... --preset classificacao` para a pessoa (os presets `atendimento` e `leitura` NÃO trazem `contact.classify`).
1. Abra `/inbox` → aba da instância atendida só pelo Hub → abra uma conversa. No cartão "Detalhes do atendimento" devem aparecer **Editar contato** e **Tipo de contato** (Cliente / Outros; sem "Interno").
2. Marque **Outros**: o botão fica marcado e a conversa muda de lista conforme o tipo. Marque **Spam** num contato de teste: a conversa sai da fila (só se ninguém a assumiu) e **Não é spam** desfaz.
3. **Cliente** → "Empresa do cliente": só aparecem empresas **já cadastradas** na instância (o diretório do ERP é a fase 04b). Sem nenhuma empresa cadastrada não dá para marcar cliente.
4. **Editar contato**: mude o apelido e o e-mail e salve.
5. Retire `contact.classify` da pessoa (`serving grant ... --preset atendimento`) e recarregue: os dois controles somem, e a API responde 403 se chamada à mão.
6. Auditoria: cada mudança fica com `acting_as = hub:<id>` e o autor real (aba Auditoria do Hub ou `audit_events`).

## J. Chamado no ERP pelo Hub (fase 04b; precisa do preset `chamados` e de uma conexão de ERP na instância) — CRIA CHAMADO DE VERDADE
Pré-requisitos: `serving ceiling ... --preset chamados` e `serving grant ... --preset chamados`; a instância precisa ter a conexão **K3G CRM** configurada em Integrações (sem ela tudo abaixo responde "chamado indisponível"). **Só faça com uma empresa de teste do ERP e avisando o cliente de teste: o passo 4 abre um chamado real e manda uma mensagem de WhatsApp.**
1. Abra uma conversa da instância atendida pelo Hub e **assuma** (o chamado só é do atendente que a assumiu).
2. No cartão aparece **Chamado**. Sem chamado: formulário com **Empresa** (lista do ERP da instância), assunto e descrição.
3. **Cliente** → "Empresa do cliente" agora mostra também as empresas do ERP (e "sugeridas por chamado anterior").
4. Abra o chamado: aparece "Chamado vinculado" com o ID externo; **não** há botão Atualizar nem Alterar status (ficam para depois). O cliente recebe o aviso com o protocolo, **uma vez**.
5. Repita o envio: "já vinculado", sem segundo chamado nem segunda mensagem.
6. Retire `ticket.create` (`serving grant ... --preset classificacao`) e recarregue: o painel de chamado some; a API responde 403 à mão.
7. Nada do que você vê (rede do navegador, respostas) contém o token ou o endereço do ERP.

## K. Transferir, devolver e anotações pelo Hub (fase 04c; precisa do preset `completo` para duas pessoas do Hub na mesma instância)
Pré-requisitos: `serving ceiling ... --preset completo` e `serving grant ... --preset completo` para a pessoa A **e** para a pessoa B (B precisa também de `grant add` com capacidade de responder). Não cria chamado nem manda mensagem ao cliente.
1. Como A, **assuma** uma conversa da instância. No cartão aparece **Transferir ou devolver**.
2. Abra: a lista mostra B (e ninguém da equipe da empresa). Escolha B e **Transferir**: a conversa some da sua lista e o responsável passa a ser B.
3. Como B, **Transferir ou devolver → Devolver à fila**: a conversa volta sem responsável. Tentar devolver de novo (ou A tentar transferir) é recusado.
4. Retire `conversation.transfer` de A (`serving grant ... --preset chamados`): o diálogo só oferece **Devolver à fila**; a API de candidatos responde 403.
5. Com `contact.note`: bloco **Anotações** no cartão; adicione, edite e remova a sua; as anotações da equipe da empresa aparecem, sem botões de edição. Sem `contact.note` o bloco não existe e a API responde 403.
6. Auditoria: transferência e devolução (`hub.conversation.transferred` / `hub.conversation.released`) ficam com o `hub_id` e o autor real; o texto das anotações nunca aparece na trilha.

## L. Tempo real e resumo por IA pelo Hub (fase 05; o resumo precisa do preset `completo_ia` e da IA da plataforma ligada)
1. Atenda uma instância pelo Hub e deixe a conversa aberta. Peça a outra pessoa (ou um número de teste) para mandar uma mensagem: ela aparece **sem recarregar** (antes só aparecia no polling de 15 s). Na aba Rede do navegador há uma conexão `inbox/events` com o cabeçalho `X-Omnira-Acting-As`.
2. Com o teto do contrato limitado a algumas filas (`service_scope`), mensagens de conversas de OUTRAS filas não geram nenhum evento para você.
3. Retire a concessão (`serving grant ... --keys none`): em até ~15 s o stream cai e a tela mostra o aviso de acesso encerrado.
4. Com `ai.assist`: o cartão mostra **Resumo por IA**; clicar gera o resumo (consome o provedor de IA da plataforma; no máximo 5 por minuto por pessoa). Sem a chave o bloco não existe e a API responde 403.
5. Auditoria: os dois eventos `ai.conversation.summarize` (antes e depois da chamada) trazem `acting_as = hub:<id>` e a pessoa.

## M. Finalizar o atendimento pelo Hub (fase 05b; precisa do preset `completo_fin`)
Pré-requisitos: `serving ceiling ... --preset completo_fin` e `serving grant ... --preset completo_fin`. **Fecha a conversa de verdade e, com "Avisar o cliente" marcado, manda a mensagem de encerramento ao cliente por WhatsApp** — use um número de teste.
1. Assuma uma conversa da instância pelo Hub. Aparece **Finalizar** (sem a chave ele não aparece).
2. Preencha motivo e resumo, adicione uma pendência (o responsável, se escolhido, tem de ser da equipe da empresa), e finalize. A conversa sai da lista de abertas e vai para "Finalizadas".
3. Conferir: chamado local da conversa fechado; chamado ligado ao ERP intacto; a ficha de encerramento (com você como autor) e a pendência aparecem para quem atender esse contato depois (membros da empresa).
4. Repita a chamada (recarregue e finalize de novo por API): mesma ficha, sem duplicar.
5. Outra pessoa do Hub, ou você numa conversa solta, **não** consegue finalizar (403).
6. Auditoria: `hub.conversation.finalized` com seu nome e o Hub; o texto do resumo nunca aparece.


## N. Memória do contato pelo Hub (fase 06; precisa do preset `completo_hist`)
Pré-requisitos: `serving ceiling ... --preset completo_hist` e `serving grant ... --preset completo_hist`. Precisa de um contato que já tenha um atendimento finalizado (de preferência com uma pendência aberta); **concluir uma pendência altera dado real da empresa**.
1. Abra uma conversa desse contato pelo Hub: o painel mostra **Pendências do contato** e **Atendimentos anteriores** (resumo, motivo, data). Sem `attendance.history` o painel diz que o histórico não está liberado.
2. Clique **Concluir** (ou Descartar) numa pendência: some da lista de abertas; um membro da empresa vê a pendência concluída por você.
3. Com um preset sem `followup.resolve` (ex.: só `attendance.history` via `--keys`), os botões não aparecem.
4. Um contato sem conversa nas filas do contrato não mostra memória.
5. Auditoria: `followup.resolved` com seu nome e o Hub; o texto da pendência nunca aparece.

## O. Rascunho de encerramento por IA pelo Hub (fase 07; precisa do preset `completo_pleno` ou das chaves `conversation.finalize` + `ai.assist`)
Pré-requisitos: `serving ceiling ... --preset completo_pleno` e `serving grant ... --preset completo_pleno`. **O texto da conversa vai ao provedor de IA da plataforma (Gemini) e a chamada gasta cota/custo real.** Use uma conversa de teste.
1. Assuma uma conversa da instância pelo Hub e abra **Finalizar**. Aparece **Sugerir com IA** (sem `ai.assist` o botão não aparece).
2. Clique: o resumo e as pendências sugeridas aparecem marcados "Sugerido pela IA: confira antes de finalizar"; **nada é gravado** até você finalizar.
3. Num preset sem `conversation.read` (ex.: `--keys ai.assist,contact.read`) o servidor recusa (403) e nada sai para o provedor.
4. Uma conversa que outra pessoa segura, ou solta, **não** gera rascunho para você (403).
