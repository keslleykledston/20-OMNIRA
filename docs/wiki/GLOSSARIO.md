# Glossário

| Termo | Significado |
|---|---|
| **Aplicação externa** | Um sistema (CRM) ligado ao OMNIRA pela API, com token e permissões próprias |
| **Atendimento** | O período em que a equipe cuida de uma conversa, de assumir até Finalizar |
| **Canal / linha** | Um número de WhatsApp conectado ao OMNIRA |
| **Claim (assumir)** | Tomar uma conversa livre para atender |
| **Conversa** | O histórico com um contato em uma linha |
| **Diário de eventos** | A lista durável e numerada de fatos (mensagem nova, mudança de titular, finalização) que o CRM consulta |
| **ETag / versão de controle** | O número "control-N" que muda quando quem atende a conversa muda |
| **Fila** | Onde as conversas esperam para serem distribuídas |
| **Finalizar** | Encerrar o atendimento e o protocolo juntos |
| **Hub** | Estrutura para uma equipe atender conversas de várias empresas |
| **Idempotency-Key** | Chave que torna seguro repetir um comando depois de uma falha |
| **Janela de 24 horas** | Prazo em que o WhatsApp oficial aceita texto livre depois da última mensagem do cliente |
| **Lease (prazo de titularidade)** | Tempo que uma aplicação pode ficar calada antes de a conversa voltar à fila |
| **Protocolo** | O número do atendimento (por exemplo `EXE-1042`) |
| **Roteador** | Quem escolhe a fila e a pessoa de cada conversa nova |
| **Setor** | Grupo de filas com o mesmo horário e responsável |
| **SLA** | Prazo combinado para responder; o sistema acompanha o relógio de cada conversa |
| **Template** | Mensagem pré-aprovada pela Meta, a única aceita fora da janela de 24 horas |
| **Titular** | Quem atende a conversa agora: pessoa, Hub ou aplicação |
| **Transbordo** | Regra que passa a conversa adiante quando ninguém atende no prazo |
| **Webhook** | Aviso que um sistema envia a outro quando algo acontece; a integração padrão do CRM não usa |
