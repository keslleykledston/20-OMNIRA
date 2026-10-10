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
