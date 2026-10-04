# ADR-0016: Mídia recebida nas conversas - captura, indexação e transcrição/descrição, com anexos tratados como hostis

## Status
Proposed (2026-10-04); decisões do dono registradas em 2026-10-04 (seção "Decisões do dono"); implementação por fatias autorizada, M1 primeiro. Escrito a pedido do dono do produto; nenhuma implementação autorizada ainda. A fatia M1
(captura, quarentena e antivírus) não depende de nenhuma escolha de IA e é a primeira candidata.

## Contexto

Hoje a mídia recebida (imagem, áudio, vídeo, documento) **não é guardada nem lida**:

- O webhook registra só uma referência (`media_ref`, `mime_type`, `size_bytes`) e a tela busca o arquivo no WAHA **sob demanda**
  (`internal/inbox/adapters/media_retrieval.go`: origem confiável fixa, redirecionamentos desligados, limite de 25 MiB, tipo
  detectado pelo conteúdo, conteúdo ativo bloqueado). Se o WAHA apagar o arquivo, ele some do OMNIRA.
- Nada disso vira texto. Uma pessoa que manda um áudio explicando o problema, uma foto do equipamento ou um PDF de boleto não
  aparece em busca nenhuma, e o resumo por IA (PRODUCT.7C1) não enxerga o que ela disse.
- A operação recebe ~250-350 mensagens por dia; mídia é uma fração relevante, e áudio é a forma mais comum de o cliente explicar.

O dono pediu: (1) receber e **indexar** imagem, vídeo e áudio; (2) **transcrever** os áudios e **descrever** as imagens;
(3) ler **anexos como PDF**; (4) usar **Gemini** ou **Copilot** como modelo; (5) com **proteção contra prompt injection e
qualquer ataque, invasão ou infecção (vírus)**.

### Fatos verificados sobre os provedores (2026-10-04)
- **Gemini API** aceita áudio, vídeo, imagem e PDF de forma nativa: PDF até 50 MB / 1000 páginas, vídeo até 2 GB (gratuito) ou 20 GB
  (pago) pela Files API ou até 100 MB inline, requisição inline até 20 MB, áudio até 9,5 h por prompt. A Files API guarda os
  arquivos por 48 h. [Documento](https://ai.google.dev/gemini-api/docs/document-processing),
  [áudio](https://ai.google.dev/gemini-api/docs/audio), [vídeo](https://ai.google.dev/gemini-api/docs/video-understanding),
  [Files API](https://ai.google.dev/gemini-api/docs/files).
- **Termos de dados do Gemini:** nos *serviços gratuitos* o Google usa o conteúdo enviado e as respostas para melhorar produtos e
  **revisores humanos podem lê-los** (a política de serviços pagos vale para todos os serviços só no EEE/Suíça/Reino Unido); nos
  *serviços pagos* o Google **não** usa prompts, arquivos nem respostas para melhorar produtos, e ativar uma conta de faturamento
  trata todo o uso como pago. [Termos](https://ai.google.dev/gemini-api/terms),
  [registro de dados](https://ai.google.dev/gemini-api/docs/logs-policy),
  [retenção zero](https://ai.google.dev/gemini-api/docs/zdr). Como o Brasil não está no EEE, **só a conta paga serve** para dados de
  clientes.
- **GitHub Copilot não tem API pública** para uso por um backend; o endpoint é destinado aos clientes oficiais (VS Code, JetBrains,
  CLI), e usá-lo como provedor genérico de modelo viola os termos do Copilot. O recurso de "traga sua própria chave" do Copilot é o
  inverso (o Copilot chamando outros provedores). [Discussão](https://github.com/orgs/community/discussions/112339),
  [termos](https://github.com/customer-terms/github-copilot-product-specific-terms).
- O servidor tem **GPU** (RTX 5060 Ti) já usada pelo `llama-server` do Hermes: transcrição de áudio **local** é viável.

## Decisão

Proposta, em fatias. A regra que organiza tudo: **todo anexo é hostil até provar o contrário, e todo texto extraído dele é dado,
nunca instrução.**

### 1. Pipeline em estágios, com quarentena
Mensagem de mídia entra → job assíncrono (`media.ingest.v1`, só `tenant_id` + `media_id`; **nunca bytes, URL assinada, token ou
segredo na fila**) → o worker segue os estágios abaixo e cada passo grava o estado em `message_media.status`:

`quarantine → scanned_clean | infected | rejected → extracted → analyzed` (ou `unreadable` / `failed`).

Só o que passa por todos os estágios anteriores avança. Falha em qualquer estágio **fecha** (não processa, não envia ao modelo).

### 2. Segurança do anexo (o pedido de proteção)
| Ameaça | Controle |
|---|---|
| Vírus / malware | **Antivírus ClamAV** (`clamd`, assinaturas atualizadas por `freshclam`) varre **antes de qualquer outro uso**. `infected` vai para quarentena, não é processado, não é enviado a modelo, não pode ser baixado, gera alerta e auditoria. |
| Arquivo de tipo perigoso | **Lista de permissão por conteúdo** (bytes mágicos, nunca o `mime`/extensão declarados): imagem (JPEG, PNG, WebP, GIF), áudio (OGG/Opus, MP3, M4A, WAV), vídeo (MP4, WebM), documento (PDF, TXT, CSV). Executáveis, scripts, atalhos, instaladores e **arquivos compactados** (ZIP/RAR/7z) são `rejected` (metadado guardado, conteúdo não). Office com macro (`docm`, `xlsm`) rejeitado; Office comum, na v1, só é guardado e varrido, **sem extração** (conversão fica para depois, num contêiner isolado). Tipo declarado diferente do real, ou *polyglot*, é `rejected`. |
| Exploração do parser (PDF, imagem, áudio, vídeo) | Extração **só em contêiner descartável**: sem rede, raiz somente-leitura, usuário sem privilégio, sem capacidades, perfil `seccomp` padrão, `tmpfs` de trabalho, limites de CPU, memória, processos e **tempo** (mata e marca `failed`). Nada é aberto no processo da API nem no host. PDF: `pdftotext`/`pdftoppm` (poppler; não executa JavaScript); PDF criptografado ou com anexos embutidos vira `unreadable`. |
| Bomba de descompressão / DoS | Tetos de bytes (25 MiB, o limite atual), pixels (12 MP), páginas (20), duração (áudio 10 min, vídeo 2 min), tamanho expandido e tempo de CPU; fila com contrapressão e **cota diária por tenant**. |
| Metadados que vazam | EXIF/GPS e metadados de documento removidos de qualquer derivado (miniatura, imagem enviada ao modelo). |
| Nome de arquivo malicioso / travessia de caminho | O nome do remetente é só rótulo exibido (sanitizado). O arquivo é gravado por **hash** (`<tenant>/<aa>/<sha256>`), nunca por nome. |
| SSRF / origem falsa | Mantém o `MediaRetriever` atual: origem WAHA fixa, sem redirecionamento, tempo limite. |
| Entrega ao navegador | Nunca por caminho direto: sempre pela API autenticada, com `Content-Disposition: attachment` (exceto imagem raster re-codificada, áudio e vídeo em `<img>/<audio>/<video>`), `X-Content-Type-Options: nosniff` e `Content-Security-Policy: sandbox`. Aviso antes de baixar documento. |
| Vazamento entre tenants | Tabela com `tenant_id` e **RLS forçada**; caminho em disco por tenant; todo acesso dentro do `TenantContext`; testes com Tenants A e B. |

### 3. Defesa contra prompt injection (texto de anexo é dado)
- **Chamadas de propósito único**: o servidor fixa a instrução ("transcreva", "descreva e extraia o texto visível"); o conteúdo do
  arquivo vai como dado delimitado. **Sem ferramentas, sem acesso a dados, sem memória, sem ações** - não há o que um
  "ignore as instruções anteriores" dentro de uma foto ou PDF possa acionar.
- **Saída com forma fixa**: JSON com esquema (`texto`, `idioma`, `confiança`), tamanho máximo, validado; o que não bate com o esquema
  é descartado e o item vira `failed`.
- **Saída é texto inerte**: exibida escapada, **sem renderizar markdown/HTML e sem transformar URL em link clicável**, rotulada
  "gerado por IA, pode conter erros", com modelo e versão do prompt gravados.
- **Nenhuma automação age sobre o texto extraído sem um passo humano.** Quando o texto de mídia entrar no resumo da conversa
  (PRODUCT.7C1), entra como `Input` não confiável, em bloco delimitado e marcado como tal.
- **Sinalização, não bloqueio**: heurística simples marca `suspicious` quando o texto extraído parece instruir um modelo ("ignore
  instruções", "você agora é..."); a operadora vê o aviso. Não se confia nela como defesa; a defesa é a arquitetura acima.
- **O que sai para o modelo é o mínimo**: só o conteúdo já limpo pelo antivírus; sem nome do cliente, telefone, tenant nem
  identificadores; nenhuma credencial na requisição além da chave do provedor, mantida no servidor.

### 4. Provedor de modelo (porta neutra)
Nova porta `MediaAnalyzer` (irmã de `TextGenerator`), com operações `Transcribe`, `Describe`, `ExtractDocument`; nenhum tipo de
fornecedor atravessa a porta. Recomendação por modalidade:

| Modalidade | Padrão recomendado | Por quê |
|---|---|---|
| **Áudio** | **Transcrição local** (Whisper na GPU do servidor) | A voz do cliente não sai da máquina; custo zero por minuto. Gemini pago como alternativa por tenant. |
| **Imagem** (descrição + texto visível) | **Gemini (conta paga)**, opt-in por tenant | Visão nativa e barata; só com conta de faturamento, pelos termos de dados. |
| **PDF** | Texto extraído localmente (`pdftotext`); **PDF escaneado** vai ao Gemini | Evita enviar o que já é texto. |
| **Vídeo** | Faixa de áudio (transcrição local) + quadros-chave (Gemini) | Limita custo e dados enviados; vídeo inteiro só se o dono pedir. |

- **GitHub Copilot: descartado.** Sem API pública para backend; seria contra os termos do produto.
- OpenAI (já escolhido para o resumo de texto, PRODUCT.7C0) permanece como segundo adaptador possível.
- Com a Files API do Gemini, o arquivo fica 48 h no Google: preferir envio *inline* (até 20 MB) e, quando for preciso usar a Files API,
  **apagar o arquivo na hora**.
- **Fecha por padrão**: sem chave, sem opt-in do tenant ou acima da cota, nada é enviado e o item fica `skipped_ai`; o resto do
  pipeline (guardar, varrer) continua funcionando.

### 5. Indexação e busca
Tabela `message_media` (RLS forçada): `tenant_id`, `message_id`, `sha256`, `kind`, `mime` real, `bytes`, `status`, estado do
antivírus, `transcript`/`description`/`extracted_text`, idioma, duração/páginas, `model`, `prompt_version`, `suspicious`.
Busca com **full-text do Postgres** (`tsvector` em `portuguese` + índice GIN), integrada ao campo de busca do Inbox, que passa a
achar também por conteúdo de áudio, imagem e documento. Vetores semânticos (pgvector) ficam como evolução, só se a busca
textual não bastar.

### 6. Armazenamento e retenção
Arquivos imutáveis por hash num diretório dedicado fora da raiz web (`/opt/...`, partição com folga), modo 0600, fora do dump do
banco. Janela quente local e **arquivo frio no disco externo**, com o mesmo desenho do G6 (cópia conferida antes de apagar, falha
do USB não perde nada, teto local, alerta de frescor). O backup do banco guarda os metadados e o texto extraído; os arquivos têm
backup próprio. Prazo de retenção e apagamento por conversa/LGPD a definir pelo dono.

### 7. Interface
Balões mostram miniatura (imagem), *player* (áudio/vídeo) ou cartão (documento) com a transcrição/descrição abaixo, recolhível e
marcada "IA". Estados claros: "analisando...", "bloqueado por segurança", "não foi possível ler", "IA desativada". O Inbox mostra
quando um trecho da busca veio de mídia. Escopo inicial: **conversas 1:1**; o mesmo pipeline serve a **grupos** depois.

## Implementação: M1 (2026-10-04)
Entregue e implantado: captura, quarentena, tipos permitidos, ClamAV, entrega segura pela API, retenção de 60 dias, métricas e
auditoria (`docs/ops/MEDIA.md`). Desvios do desenho, por motivo medido:
- **Fila**: em vez de um job NATS (`media.ingest.v1`), a linha `message_media` nasce por *trigger* na mesma transação da mensagem e o
  worker a reivindica por consulta (`FOR UPDATE SKIP LOCKED`, a cada 2 s). Motivo: o WAHA apaga o arquivo em poucos minutos e o caminho
  outbox→NATS adicionaria latência e mais um ponto de falha; a garantia (durabilidade, sem perda, sem dupla execução) é a mesma.
- **Achado**: o `media_ref` guardado aponta para `http://localhost:3000/...` (visão do WAHA) e a busca ao vivo nunca funcionou a partir de
  outro contêiner; além disso o arquivo some do WAHA em minutos. As 348 mídias recebidas antes da M1 (183 áudios, 142 imagens, 18
  documentos, 5 vídeos) **não podem ser recuperadas** e ficaram `source_gone`.
- Provado ao vivo com WAHA e ClamAV reais: imagem e áudio limpos, EICAR `infected` (`Eicar-Test-Signature`), ZIP disfarçado de JPG, HTML e
  PDF com JavaScript `rejected`, arquivo ausente `source_gone`; só os limpos chegam ao navegador.

## Plano em fatias
- **M1 - captura, quarentena e antivírus** (sem IA): tabela, fila `media.ingest.v1`, download pelo `MediaRetriever`, hash, tipo real,
  lista de permissão, ClamAV, armazenamento por hash, entrega segura pela API. Testes de tenancy, de arquivos hostis (EICAR,
  *polyglot*, tipo falso, bomba de pixels) e de falha do antivírus (fecha).
- **M2 - áudio local**: Whisper na GPU, sandbox, limites, transcrição indexada.
- **M3 - imagem**: adaptador Gemini (conta paga, opt-in), descrição + texto visível, remoção de EXIF.
- **M4 - PDF**: extração em sandbox; PDF escaneado via modelo.
- **M5 - busca** integrada ao Inbox e **M6 - interface** (mídia + transcrição).
- **M7 - vídeo** (áudio + quadros-chave) e **M8 - retenção fria**.
- Grupos: reaproveitam o pipeline depois.

## Consequências
- **Segurança**: a superfície nova é grande (arquivos de desconhecidos). Por isso a ordem é segurança primeiro (M1) e IA depois; nenhum
  arquivo chega a um parser ou a um modelo sem antes passar por antivírus e lista de permissão, e nenhum parser roda fora do
  contêiner descartável.
- **Privacidade / LGPD**: enviar mídia de clientes a um terceiro exige base legal, aviso e *opt-in* por tenant. Áudio local reduz isso
  ao mínimo. O dono precisa decidir texto de aviso e retenção.
- **Custo**: chamadas pagas só com cota diária por tenant e teto de tamanho/duração; sem chave ou acima da cota, fecha.
- **Operação**: novos componentes (ClamAV, contêiner de extração, Whisper) consomem memória (o ClamAV costuma pedir ~1 GB) e GPU
  compartilhada com o Hermes; precisam de monitoramento e de entrada no plano de capacidade.
- **Risco residual**: modelo pode errar a transcrição/descrição (por isso o rótulo "IA") e antivírus não pega tudo (por isso a
  extração em sandbox e o tipo restrito são camadas independentes).
- Exige OpenAPI/AsyncAPI, telemetria (contadores por estágio e resultado, latência, custo) e auditoria (`media.*`).

## Alternativas
- **Copilot como motor**: sem API pública e contra os termos. Rejeitada.
- **Enviar tudo ao Gemini, inclusive áudio**: mais simples, mas leva a voz do cliente a terceiros e custa por minuto. Mantida como opção por tenant, não como padrão.
- **Só OpenAI**: viável (transcrição e visão existem), porém não cobre vídeo nativamente e concentra tudo num fornecedor.
- **Processar no processo da API**: mais rápido de fazer e inaceitável para arquivos hostis. Rejeitada.
- **Aceitar ZIP/Office com macro e "analisar depois"**: abre exatamente a porta que se quer fechar. Rejeitada na v1.
- **Não guardar o arquivo, só o texto**: reduz risco e disco, mas perde a evidência original e impede reprocessar com modelo melhor. Rejeitada; o arquivo é guardado em quarentena/varrido e segue política de retenção.

## Decisões do dono (2026-10-04)
1. **Áudio: transcrição local** (Whisper na GPU do servidor). Nada de áudio sai da máquina.
2. **Gemini: conta de faturamento criada pelo dono; orçamento inicial US$ 10,00/mês** para medir. O teto vira cota por tenant e trava global: ao atingir, a análise por Gemini fecha (`skipped_ai`) até o mês virar.
3. **Opt-in por tenant**: o dono pediu explicação antes de decidir (ver resposta no chat). Até decidir, a IA externa fica desligada; áudio local e M1 não dependem disso.
4. **Retenção: arquivo 60 dias; a transcrição/descrição permanece na conversa** mesmo depois que o arquivo for removido. Texto extraído é parte da mensagem, não do arquivo.
5. **ClamAV autorizado** (software livre, sem custo de licença; ~1 GB de RAM, há ~10 GB livres).
6. **Vídeo fica para a segunda entrega. Office (docx/xlsx): rejeitado na v1, conversão em contêiner no futuro.**
7. **Limites propostos aprovados**: imagem 12 MP, PDF 20 páginas, áudio 10 min, vídeo 2 min, 25 MiB.

## Questões em aberto
- Item 3 (opt-in por tenant e texto de aviso ao cliente).

1. **Áudio local** com Whisper na GPU (recomendado) ou Gemini pago?
2. **Gemini conta paga**: quem cria a conta de faturamento e a chave, e qual o **orçamento mensal** máximo?
3. **Opt-in por tenant** com a IA de mídia desligada por padrão (recomendado) e qual o texto de aviso ao cliente (LGPD)?
4. **Retenção** dos arquivos e da transcrição (dias), e se apagar a conversa apaga a mídia.
5. **ClamAV** pode entrar na `docker-compose` (~1 GB de RAM)?
6. **Vídeo** na primeira entrega ou depois? **Office** (docx/xlsx): rejeitar ou converter em contêiner no futuro?
7. Limites propostos (imagem 12 MP, PDF 20 páginas, áudio 10 min, vídeo 2 min, 25 MiB) servem?
