# Perguntas frequentes

Use a busca da página para achar uma palavra. Cada resposta diz onde ver o passo a passo.

## Atendimento

### Por que não consigo responder uma conversa?

Quase sempre é um destes casos:

1. **Ninguém assumiu** e a conversa está "Sem atendente": clique em **Assumir**.
2. **Outra pessoa atende**: só quem está com a conversa responde. Peça que ela transfira, ou um supervisor retome.
3. **Um CRM está com a conversa** (aparece "Aplicativo externo"): enquanto o aplicativo atende, o OMNIRA não deixa a equipe responder por cima. Um supervisor com a permissão certa pode **retomar** a conversa, informando o motivo.
4. **A conversa foi finalizada**: está em **Encerradas**, só para consulta. Se o cliente escrever, abre um atendimento novo.
5. **Janela de 24 horas fechada** (só no número oficial da Meta): só vai mensagem de **template** aprovado.
6. **Você não tem permissão** de responder: fale com o administrador.

### O que é o protocolo? Quando nasce e quando termina?

É o número do atendimento (por exemplo `EXE-1042`). Nasce quando alguém assume (ou quando o sistema distribui a conversa) e **termina junto com o atendimento, ao Finalizar**. Aparece em *Protocolo e tarefas* e como uma linha na conversa.

### O cliente recebe o número do protocolo?

Depende de dois interruptores: a empresa precisa ter o **aviso de protocolo** ligado, e a caixa **Informar o número do protocolo ao cliente** precisa estar marcada no Finalizar. Quando um CRM finaliza pela API, ele pode pedir para **não** avisar (`notify_protocol: false`) se já manda a própria mensagem de encerramento.

### Qual a diferença entre Soltar, Transferir e Enviar para uma fila?

- **Soltar** devolve a conversa à fila: ninguém a segura e o roteador distribui de novo.
- **Transferir** passa a conversa para **uma pessoa** (um técnico) escolhida na lista.
- **Enviar para uma fila** passa a conversa para **outro setor**; as regras da fila escolhem quem atende.

### O que significam "Equipe", "Automação", "Aplicativo externo" e "Celular da empresa"?

São os rótulos de **quem falou** nas mensagens da empresa: uma pessoa, o assistente automático, um CRM ligado pela API, ou alguém que respondeu direto do celular do número (fora do OMNIRA).

### O que são as linhas cinzas no meio da conversa?

São **eventos do atendimento**: quem assumiu, atribuiu, soltou, transferiu, enviou para fila, retomou de um aplicativo, quando um aplicativo externo assumiu ou liberou, o protocolo aberto e encerrado, e a finalização (com o motivo). Não são mensagens para o cliente.

### Por que às vezes o nome de um colega não aparece nessas linhas?

Quem só atende vê o **próprio** nome e "Um atendente" para colegas; supervisores e administradores, que podem consultar a equipe, veem os nomes. É de propósito, para não expor a equipe sem necessidade.

### O que é a janela de 24 horas?

No WhatsApp **oficial (Meta)**, a empresa só pode mandar texto livre até 24 horas depois da última mensagem do cliente. Depois disso só vai **template** aprovado pela Meta. A tela mostra quanto tempo falta e oferece **Enviar template**. Em número não oficial (sessão do aplicativo) essa regra não vale.

### Posso mandar foto, PDF, áudio ou vídeo?

Sim, pelo clipe (**Anexar arquivo**), **um arquivo por mensagem**, com o texto como legenda. O sistema confere o tipo e o tamanho pelo conteúdo, remove dados escondidos da foto (como a localização) e passa pelo antivírus; um arquivo recusado mostra o motivo. A empresa pode desligar os anexos de saída. O envio por WhatsApp real ainda está em validação final na operação.

### Como vejo o que já aconteceu com este contato?

Em **Detalhes do atendimento**, a aba **Histórico** e a seção **Histórico do atendimento** mostram os atendimentos anteriores, com o resumo de cada um e o que ficou pendente.

### As mensagens de grupos de WhatsApp aparecem nas conversas?

Não. Grupos ficam em **Grupos**, só para leitura. Nenhum atendimento nasce de um grupo.

## Contatos e tickets

### O que significa "Não classificado"?

Ninguém disse ainda se a pessoa é cliente, interno ou outro. O atendimento fica limitado até a classificação. Veja o [Tutorial 4](../tutoriais/04-classificar-contatos-e-spam.md).

### Marquei spam sem querer. E agora?

Nada foi apagado. Abra a conversa na caixa **Spam** (menu **Mais...** da lista) e use **Não é spam** ou **É cliente**.

### Por que o total dos tickets ou das pessoas não muda quando passo de página?

O total é contado **uma vez**, ao abrir a primeira página, e se mantém nas seguintes. Se você mudar um filtro, ele é contado de novo.

### Posso ordenar por qualquer coluna?

Em Tickets: **Assunto**, **Prioridade** e **Atualizado**. Em Pessoas: **Pessoa** (nome). No celular, pelo menu **Ordenar por**. As outras colunas ainda não ordenam.

### Mudei o filtro e voltei para a primeira página. É normal?

Sim. A posição na lista só vale para aquela combinação de filtro e ordem.

### Onde está "Relatórios"?

Ainda não existe no produto; a tela fica oculta até ter dados reais.

## Administração

### Como crio um setor ou uma fila?

**Setores e horários** → **+ Novo setor** ou **+ Nova fila**. Veja o [Tutorial 5](../tutoriais/05-filas-e-setores.md).

### Para onde vão as conversas novas de um número?

Para a fila escolhida em **Entrada por canal**; sem escolha, para a **fila padrão**. Conversas que já estão numa fila não mudam.

### O atendente vê todas as filas?

Depende da empresa. Com a **visibilidade por setor** ligada, o atendente vê só as filas dele, as conversas dele e as sem fila; supervisores e administradores veem todas.

### O que acontece fora do horário?

A conversa espera e o cliente pode receber o **aviso de horário** configurado na fila (botão **Avisos de horário**). O que fazer com o que sobrar depende das regras de **Transbordo e SLA**.

### Como conecto um número de WhatsApp?

Em **Canais** → **Adicionar canal**. O passo a passo com capturas do assistente entra na próxima rodada desta documentação.

## Integrações e API

### Preciso de um endereço público (webhook) no meu CRM?

**Não.** O CRM **consulta** o diário de eventos (`GET /events?after=<seq>`) a cada 2 a 3 segundos. O OMNIRA sabe entregar webhooks assinados a quem pedir, mas a integração padrão não usa.

### Com que frequência consultar?

A cada 2 a 3 segundos, com `limit=200`. Um fato aparece em cerca de meio segundo depois da mudança. Isso gasta uns 25 pedidos por minuto, bem abaixo do limite.

### O que é `Idempotency-Key`?

Um identificador que **você** cria para cada intenção ("mandar este texto nesta conversa"). Se a chamada falhar por rede e você repetir com a **mesma chave**, o OMNIRA devolve a mesma resposta e não duplica a mensagem. Chave nova a cada tentativa é erro.

### O que é `If-Match`?

A **versão de controle** da conversa que você leu (`ETag: "control-N"`). Se a conversa mudou, a resposta é `412 stale_control_version`: releia e decida de novo. Não repita às cegas.

### Recebi `409`. O que fazer?

Leia o `code`: `not_holder` (você não segura a conversa: faça `claim`), `held_by_other` (outro titular), `conversation_closed`, `outside_hours` (fora do horário do setor e a aplicação não tem permissão). O guia da API traz a tabela completa.

### O `202` quer dizer que a mensagem foi entregue?

Não. `202` quer dizer **na fila de envio**. A entrega vem nos fatos `message.status_changed.v1` (`sent`, `delivered`, `read`, ou `failed`).

### Qual o limite de chamadas?

Padrão de **600 por minuto** por aplicação. Passou: `429` com `Retry-After`; espere esse tempo.

### Posso abrir uma conversa nova por telefone?

**Ainda não.** A API responde a conversas que já existem. Abrir conversa ativa (alertas, por exemplo) exige template aprovado e consentimento, e ainda está em planejamento.

### Posso mandar template?

Sim, com `{"template": {"id": "<uuid>", "params": [...]}}`. Hoje não há rota para **listar** os templates aprovados: peça os `id` ao administrador.

### Como o CRM lê a foto que o cliente mandou?

Com o escopo `media.read`: a mensagem traz `media.id` e, com `status: clean`, `GET /media/{id}` devolve o arquivo. Enquanto confere, `409 media_pending`.

### Como mando um arquivo pelo CRM?

Suba com `POST /conversations/{id}/attachments` (a aplicação precisa estar com a conversa) e envie com `POST /messages` e o `attachment_id`; o texto vira legenda. Limites e erros no guia.

### O que acontece se o CRM cair com conversas na mão?

Se a aplicação tem **prazo de titularidade** (lease), a conversa volta para a fila depois de o prazo mais 2 minutos sem sinal. Sem prazo, ela fica com a aplicação até alguém soltar. O administrador pode **Suspender** a aplicação: as conversas voltam na hora.

### Quem é o "atendente" do CRM para o OMNIRA?

Ninguém: quem atende, para o OMNIRA, é a **aplicação**. O nome do atendente do CRM vai no campo opcional `external_agent_id`, só como rótulo de auditoria; não dá acesso a nada.

### Os grupos de WhatsApp aparecem na API?

Não. A API só devolve conversas com contatos.

### O que o CRM não consegue fazer hoje?

Abrir conversa por telefone, buscar ou criar contato, nota interna, lido/não lido (o CRM calcula pelo diário), listar templates e etiquetas livres (setor é fila). O guia da API tem a tabela.

## Segurança e privacidade

### O token aparece de novo?

Não. É mostrado **uma vez**, na criação. O OMNIRA guarda só um resumo. Perdeu? **Gerar novo token** e revogue o antigo.

### Suspender ou revogar?

**Suspender** é temporário: corta o acesso e devolve as conversas; dá para reativar. **Revogar** é definitivo.

### Um CRM vê todas as conversas da empresa?

Só as conversas da **empresa da aplicação**, que **não** sejam conversas com a equipe, de contato classificado como interno ou spam, e, se o administrador limitou a aplicação, só as das filas e números permitidos. O que a aplicação não pode ver responde como "não encontrado".

### O que a aplicação vê de cada pessoa?

Não vê nomes de atendentes: o titular aparece só como tipo (pessoa, Hub ou aplicação) e "é minha". Os fatos do diário trazem **referências**, não o texto das mensagens nem telefones.

### Onde ficam os dados de uma empresa em relação às outras?

Cada empresa só acessa os próprios dados: o servidor confere a empresa em **toda** consulta e o banco aplica uma segunda barreira. Aplicações externas nunca escolhem a empresa por parâmetro.
