# Visão geral: a tela do OMNIRA

O OMNIRA reúne as conversas de WhatsApp da sua empresa em um só lugar, distribui cada conversa para a fila certa e deixa a equipe atender, registrar o protocolo e fechar o atendimento.

![Tela de Conversas](../imagens/01-conversas.png)

## O menu da esquerda

| Grupo | Item | Para que serve |
|---|---|---|
| Operação | **Dashboard** | Visão geral: conversas abertas, tickets, contatos e filas que pedem atenção |
| | **Conversas** | Onde se atende. Lista, conversa e detalhes na mesma tela |
| | **Tickets** | Solicitações ligadas às conversas |
| | **Reconciliação** | Conferência de tickets com sistemas externos (uso do administrador) |
| | **Contatos** | Pessoas que já falaram com a empresa e a equipe interna |
| | **Supervisor** | Acompanhamento da operação (para supervisores) |
| Automação e canais | **Automação** | Fluxos automáticos (menu de boas-vindas, avisos) |
| | **Canais** | Os números de WhatsApp conectados |
| | **Grupos** | Grupos de WhatsApp (somente leitura) |
| Configuração | **Pessoas** | Quem usa o sistema e com qual papel |
| | **Setores e horários** | Filas, setores, horários de atendimento, escalas e SLA |
| | **Integrações** | Aplicações externas (CRMs) que usam a API |
| | **Configurações** | Ajustes da empresa |

Você só vê o que o seu papel permite. Se um item abre uma tela de "Acesso restrito", peça a permissão ao administrador.

## A barra de cima

- **Buscar (Ctrl+K)**: abre a busca rápida de telas, contatos e conversas. Veja o [Tutorial 2](../tutoriais/02-buscar-e-navegar-com-ctrl-k.md).
- **Seu nome e Sair**.
- Em celular, a barra de baixo traz Conversas, Contatos e Canais.

## Quem pode fazer o quê

As permissões vêm do **papel** da pessoa (administrador, supervisor, atendente). O sistema nunca esconde um dado só pela tela: o servidor confere de novo a cada pedido. Detalhes de papéis: próxima rodada da documentação.

## Conceitos que aparecem o tempo todo

- **Conversa**: o histórico com um contato em uma linha (número) de WhatsApp.
- **Atendimento**: o período em que a equipe cuida da conversa, do momento em que alguém assume até Finalizar.
- **Protocolo**: o número do atendimento (por exemplo `EXE-1042`). Nasce ao assumir e encerra ao Finalizar.
- **Fila**: onde as conversas esperam. **Setor**: um grupo de filas com o mesmo horário.
- **Titular**: quem está atendendo agora. Pode ser uma pessoa ou uma aplicação externa (CRM).

Mais termos no [Glossário](../GLOSSARIO.md).
