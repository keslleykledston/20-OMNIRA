# Mapeamento de tratativas (NOC/ISP) — v1

Insumo exploratório, não é ADR. Objetivo: entender como clientes/parceiros da K3G
pedem coisas em conversas reais de WhatsApp (grupo e 1:1), pra eventualmente informar
uma IA de triagem — o observador de grupo (ADR-0055) classifica categoria+resumo por
mensagem; isso aqui vai um passo além, mapeando intenção → ação concreta.

Nenhum código foi escrito a partir deste documento ainda. Decisões de arquitetura
(nova tabela, novo pipeline, o que automatizar) ficam pra um ADR próprio, reservado
desde a ADR-0015 item 7.

## Fontes analisadas

1. **Grupo técnico multi-empresa**: K3G ↔ MS~FIBRA, 4wnet/Stenio, Grupo Datacom/Fernando
   (export `.zip`, 18/04/2026–11/2026).
2. **Conversa 1:1 com cliente direto**: Keslley ↔ Stenio (4wnet - Provedor Bvb)
   (export `.zip`, 27/03/2026–09/11/2026, 8 meses).

**Caveat estrutural recorrente**: os dois exports do WhatsApp vieram com conteúdo
duplicado (merge de backups sobrepostos — no primeiro 2x, no segundo 5x), sempre com
uma cauda final única não repetida. Se isso virar pipeline de ingestão automatizada,
precisa de deduplicação por (autor, timestamp, corpo) antes de processar.

## Taxonomia de tratativas

| # | Tratativa | Exemplo real | Ação típica da K3G |
|---|---|---|---|
| A | Instabilidade/lentidão genérica | "Estamos com instabilidade no serviço" | pedir ipvfoo/print, checar flap, "em verificação" → "normalizando" |
| B | Conteúdo específico degradado (CDN) | "Google esta com video travando" | checar se é só aquele conteúdo, manobrar vlan/upstream |
| C | Pedido de anúncio de bloco/ASN (BGP) | "Podem anunciar pro link ip" + bloco | anunciar nos filtros, às vezes abrir chamado com upstream |
| D | Cobrança de status de pedido em aberto | "Esse bloco ja esta anunciado?" | "estamos cobrando as operadoras" |
| E | Pedido de diagnóstico técnico | "manda o ipvfoo", "tracert por favor" | roda comando, analisa junto (muitas vezes termina em chamada de voz) |
| F | Queda total de circuito | "Nosso link parou" | "eventão... manobrando", causa raiz depois |
| G | Onboarding de cliente/trânsito novo | "posso liberar o Valderlan?", bloco+ASN | libera nos filtros, confirma trânsito vs CDN |
| H | Escalonamento por SLA estourado | "urgente ⚠️⚠️⚠️", "2h sem link" | resposta prioritária, Keslley entra pessoalmente |
| I | Manutenção coordenada/manobra agendada | "Madrugada será a manobra?" | agenda janela, avisa "validem" |
| J | Check-in proativo da K3G (sentido inverso) | K3G pergunta sem provocação | — (fora do escopo de "pedido do cliente") |
| K | Infra física | fotos de poste/porta | confirma com fotos |
| L | Pedido de melhoria de processo/ferramenta | "criar communities pra gente gerenciar" | — (não é incidente) |
| M | Decisão/autorização de mudança de topologia | "Não seria interessante desativar a Tim mns?" | decisão de engenharia conjunta, não incidente |
| N | Cobrança por silêncio (sentido inverso de H) | "Stenio.. seu silêncio não me ajuda a te ajudar.." | K3G cobrando responsividade do cliente |
| O | **Resolvido fora do texto** | ligação de voz/vídeo, mídia oculta, "Mensagem apagada" após resolver por voz | não classificar por categoria — só registrar que a tratativa existiu e foi fechada fora do canal de texto |

A categoria **O** não é um caso de ambiguidade a mais — é um bucket próprio, por decisão
explícita: ~7% do volume da conversa 1:1 é só "Ligação de voz, X minutos" sem texto
algum, e boa parte do diagnóstico técnico (E) também migra pra chamada no meio do
atendimento. Forçar classificação de texto nesses casos geraria ruído.

## Padrão de escalonamento

Pergunta → silêncio → "Algum retorno?" repetido → @menção direta ao Keslley → só então
reclamação explícita de demora (normalmente após 30–50min sem resposta). Emoji
⚠️⚠️⚠️ marca a virada de "relato" pra "cobrança formal" — usado de forma consistente
por mais de um parceiro, não é idiossincrasia de uma pessoa só.

## Casos ambíguos em aberto

- Mensagens cuja única evidência é mídia oculta (print, gráfico, tracert) — tratadas
  como categoria O, não como "pedido não identificado".
- Cliente discorda do diagnóstico da K3G mas segue a ação mesmo achando que não é a
  causa raiz (ex.: "Vamos retirar mas não vejo que esse é o problema") — não é bem
  "pedido atendido" nem "objeção ignorada"; fica como terceira categoria própria se
  isso virar um pipeline automatizado de verdade, não decidido ainda.
- Trechos de config/community BGP colados em bloco (até ~3770 linhas seguidas num dos
  exports) quebram qualquer classificação por linha — a unidade real ali é o bloco
  inteiro, não a mensagem individual.

## Validação orgânica da ideia (achada no próprio histórico)

Duas falas, meses antes dessa análise começar, já verbalizavam exatamente a
necessidade que motivou este exercício:

> Keslley, 09/07/2026: "e teria q ter uma base de conhecimento pra saber quem
> manobrar pra alcançar o que quer."

> Stenio, 01/06/2026, depois de uma solução dar certo: "foi deu certo obrigado,
> bota na cabeca da IA"

## Próximos passos (não decididos)

- Mais exports de perfis menos técnicos (cliente comercial, não-ISP) ficam em aberto
  caso o escopo da IA de triagem precise cobrir além do universo NOC — decisão adiada
  por ora, os 2 exports técnicos já cobrem o caso de uso principal.
- Se e como isso vira um ADR próprio (pipeline de ingestão de backups, geração de
  exemplos few-shot, fila de revisão de ambiguidade) é decisão futura, fora do escopo
  deste documento.
