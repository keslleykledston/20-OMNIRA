# Automação de atendimento — guia de uso

Para quem administra o atendimento. Os fluxos conversam com o contato antes de uma pessoa assumir: identificam quem é, descobrem
o assunto, abrem o chamado e entregam a conversa à fila certa, com o resumo pronto. **Nada vale até você publicar**, e publicar
nunca altera uma conversa que já está em andamento.

> Estado: **LAB**. A funcionalidade vem **desligada** (`OMNIRA_FLOWS_ENABLED=false`). Veja `OPERATIONS.md` para ativar e `PRODUCTION-GATE.md` para o que falta antes de uso real.

## Começar rápido: instalar um pack

1. **Automação → Modelos e packs.** Escolha o perfil (provedor de internet, empresa de TI ou geral): o OMNIRA recomenda os packs.
2. **Instalar pack.** Marque o que quer (itens opcionais vêm desmarcados; o que um modelo precisa é incluído sozinho).
3. **Mapeie as filas.** Cada atendimento precisa de uma fila sua (suporte, financeiro, comercial, NOC…). Cada fila é pedida **uma vez**, mesmo usada por vários modelos.
4. **Confirme.** Tudo é criado como **rascunho seu**. Se um nome já existir, o novo ganha o sufixo `-2` e o seu não é tocado.

| Pack | Para quem | O que traz |
|---|---|---|
| OMNIRA Starter | qualquer operação | recepção, contato desconhecido, empresa do atendimento, chamado existente, transferir para humano, fora do horário, pesquisa de satisfação |
| K3G Support | empresa de TI e redes | recepção com horário comercial, VPN, firewall, solicitação de mudança, triagem técnica geral |
| ISP NOC | provedor de internet | link fora, lentidão, intermitência, BGP/roteamento, DNS, equipamento e ONU offline, incidente coletivo e crítico, financeiro, comercial |

## Revisar, testar e publicar

1. Abra cada fluxo em **Automação → Fluxos**. Ajuste textos, horários e filas.
2. **Problemas** (aba lateral) mostra, em tempo real, o que o servidor não aceita. **Erros bloqueiam a publicação; avisos não.**
3. **Simular** roda um cenário (quem escreve, canal, empresas, o que o contato responde, passagem de tempo). Nada é enviado nem gravado: você vê as mensagens que o bot mandaria e o que ele faria (chamado, fila, transferência).
4. **Salve** o rascunho e **Publique**, nesta ordem: **primeiro os subfluxos** (contato desconhecido, empresa, chamado existente…) e **por último o fluxo de entrada**, porque a publicação fixa a versão de cada subfluxo.
5. Em **Configurações do fluxo**, defina prioridade, se é o padrão, quando inicia e **em quais linhas de canal** vale (WhatsApp por WAHA e oficial convivem).

Voltar atrás: **Versões → Ativar**. Só as próximas conversas usam a versão escolhida.

## Como o bot se comporta (regras que valem sempre)

- **Contato desconhecido não vira cliente.** O bot só pergunta nome e empresa e **não cria empresa**: quem classifica é uma pessoa.
- **Várias empresas: o bot pergunta.** Ele nunca escolhe a primeira nem a "principal".
- **Prioridade do chamado é regra do fluxo**, nunca texto do contato e nunca IA. "Crítico" só sai de regras que você pode ler e mudar.
- **Quando um atendente assume, o bot cala** na hora. Se o fluxo termina sem entregar a conversa, ela **volta para a fila normal**: ninguém fica sem resposta.
- **Toda espera tem saída por tempo.** Uma resposta inválida é repetida até 3 vezes e depois segue a saída "sem resposta".
- **Janela de 24 h do WhatsApp oficial:** fora dela o bot **não envia texto livre**; o nó segue a saída "janela de 24h fechada".
- **Nada de mudança em equipamento.** Os modelos de firewall e mudança só **registram** a solicitação.

## Execuções

**Automação → Execuções** lista o que aconteceu, com o passo a passo de cada conversa e o resumo entregue ao atendente. Os
indicadores são **medidos**: sem execuções, tudo aparece zerado ou "—" (nada é estimado). Segredos digitados pelo contato
aparecem mascarados.

## IA (opcional, desligada por padrão)

Os nós de IA **só sugerem**: classificar intenção, extrair dados e resumir a conversa. Com pouca confiança, ou se a IA estiver
desligada ou fora do ar, o fluxo segue as saídas "baixa confiança" e "erro", que **você precisa ligar** (o publicador exige).
Eles precisam de `OMNIRA_FLOWS_AI_ENABLED=true` e do modelo da plataforma configurado.

## Permissões

| O que | Permissão |
|---|---|
| Ver fluxos, versões e problemas | `flow.view` |
| Criar / editar o rascunho | `flow.create` / `flow.edit` |
| Simular | `flow.test` |
| Publicar e voltar de versão | `flow.publish` (separada de editar, de propósito) |
| Arquivar | `flow.archive` |
| Ver / instalar modelos e packs | `flow_template.view` / `flow_template.install` |
| Ver execuções e indicadores | `flow_run.view` |

Por padrão o administrador tem todas; o supervisor só vê e simula; o agente não tem nenhuma.
