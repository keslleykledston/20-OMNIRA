# Para o dono: o que fazer antes de entregar o pacote ao time do CRM

Este arquivo **não vai** no pacote do CRM.

## 1. Escolha onde o CRM testa

Recomendado: **Keslley_Pessoal** (empresa de teste: filas Default, Comercial, Financeiro e Suporte; WhatsApp WAHA ativo). Nada de cliente real. A K3G_Solutions só depois do teste verde (`HEADLESS-P2-K3G.md`).

## 2. Crie a aplicação (Configurações > Integrações > Aplicações externas > Nova aplicação)

A "CRM Piloto escrita" que existe lá não serve para o teste completo: **não tem** `conversation.transfer`, `conversation.finalize` nem `media.read`. Crie uma nova:

| Campo | Valor |
|---|---|
| Nome | `CRM teste` |
| Escopos | `conversation.read`, `message.read`, `contact.read`, `media.read`, `events.read`, `conversation.claim`, `message.send`, `conversation.transfer`, `conversation.finalize` (**não** marque `service.read`) |
| Taxa | **não se edita na tela**: nasce em 600 por minuto (o valor aparece em Gerenciar) |
| Lease (prazo de titularidade) | **não se define na tela**: nasce sem prazo (a conversa fica com o CRM até ser devolvida ou a aplicação ser suspensa). Para ter prazo (ex.: 900 s) ou outra taxa (ex.: 300 na fase de leitura da K3G_Solutions), peça a mim: a API de administração aceita `lease_seconds` e `rate_limit_per_min`. Sem isso o passo 17 do roteiro (perda de lease) não é testável |
| Fora do horário | permitido, só neste teste (senão o `claim` dá `409 outside_hours` fora do expediente da fila) |
| Filas/linhas | sem filtro (todas as da empresa de teste) |

O token (`omn_ext_<id>.<segredo>`) aparece **uma vez**. Entregue por canal seguro (não por chat comum). Quando acabar: **suspenda** a aplicação na tela; a antiga "CRM Piloto escrita" pode ser revogada.

## 3. Prepare o teste

- Telefone de teste mandando mensagem para a linha WhatsApp da Keslley_Pessoal.
- Arquivos: uma **foto com GPS** nos metadados, um **PDF**, uma **nota de voz** (.ogg/.mp3), um **vídeo curto** (.mp4) e um arquivo de texto com a **linha de teste de antivírus (EICAR)**.
- Você (ou um supervisor) na tela do OMNIRA durante os passos 5, 6, 15 e 18.
- O envio de anexos de saída **nunca foi provado ao vivo**. Os passos 10-12 são essa prova. Se algo falhar ali, é correção do lado do OMNIRA, não do CRM.

## 4. Antes de entregar

- [ ] Aplicação criada e token guardado em local seguro.
- [ ] Combinar com o time do CRM quem opera e o horário (precisa de você com o telefone).
- [ ] Mandar o pacote (`omnira-crm-handoff-2026-10-18.zip`) e o token em canais **separados**.
- [ ] Confirmar com o CRM: o inbox dos alertas do Gateway usa o **mesmo número** do atendimento? (decide a ordem da fase D.)

## 5. Para a K3G_Solutions depois (não fazer agora)

1. Criar as filas que faltam: **Suporte TI** (ou confirmar que "Suporte" serve), **Provedores Telecom**, **Servidores**, cada uma com horário e membros (Setores e horários).
2. Aplicação nova, **só leitura** primeiro (taxa 300), 24 h; depois escrita numa fila só.
3. `OMNIRA_HEADLESS_WRITE_ENABLED` já está ligado em produção. Desfazer: suspender a aplicação, depois a variável, depois revogar.

## 6. Limites que você deve conhecer

- Sem webhook; o CRM consulta o diário.
- Fato `conversation.closed.v1` causado pelo CRM chega com `mine=false` (o CRM reconhece pela resposta do `finalize`).
- Templates aprovados não têm rota de listagem: se o CRM precisar enviar template (fora das 24 h no WhatsApp oficial), você passa os `template.id`.
- Grupos de WhatsApp, abrir conversa por telefone e busca de contato não existem na API.
