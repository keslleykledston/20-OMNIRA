# Transcrição local de áudio (ADR-0016, M2)

O áudio dos clientes **nunca sai do servidor**. A transcrição roda em um serviço do host, na GPU, isolado do resto.

## Peças
| Peça | Onde |
|---|---|
| Motor | `whisper.cpp` v1.9.4 compilado com CUDA (arquitetura 120, RTX 5060 Ti), `/opt/omnira-whisper/bin/whisper-server` |
| Modelo | `ggml-large-v3-turbo-q5_0.bin` (574 MB; SHA-256 conferido contra o publicado no Hugging Face) |
| Serviço | `omnira-whisper.service` (systemd; versão em `deploy/whisper/`), escuta em `172.17.0.1:18181` (gateway do docker0) |
| Cliente | worker (`internal/media/adapters/whisper.go`), variável `OMNIRA_WHISPER_URL` (padrão `http://172.17.0.1:18181`) |
| Fila | tabela `message_media_analysis` (kind `transcript`), criada por *trigger* quando o áudio é liberado pelo antivírus |

## Isolamento (o servidor processa áudio hostil)
- Usuário dedicado `omnira-whisper`, sem login nem home; arquivos do serviço pertencem ao root e são somente leitura para ele.
- systemd: `NoNewPrivileges`, `CapabilityBoundingSet=` vazio, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
  `SystemCallFilter=@system-service`, `RestrictAddressFamilies=AF_INET AF_UNIX`, sem `/data`, `/mnt`, `/opt/omnira-media` etc.
  (`InaccessiblePaths`), dispositivos só da GPU, `MemoryMax=6G`, `TasksMax=256`. `systemd-analyze security` = **1,8 (OK)**.
- **Sem internet**: `IPAddressDeny=any` e permitido apenas loopback, `172.17.0.1/32` e a rede do compose (`192.168.128.0/20`).
  Se a sub-rede do compose mudar, atualize `IPAddressAllow` e a regra do UFW.
- `ffmpeg` só roda dentro do serviço, por um *wrapper* (`/opt/omnira-whisper/bin/wrap/ffmpeg`) que impõe: sem protocolos de rede
  (`-protocol_whitelist file`), entrada limitada a 10 min (`-t 600`), 1 thread, sem stdin.
- Firewall: `ufw allow from 192.168.128.0/20 to 172.17.0.1 port 18181 proto tcp` (comentário "OMNIRA worker -> local Whisper").
- O texto devolvido é dado não confiável: limpo (controles/bidi removidos, 20 000 caracteres), filtrado contra frases-fantasma de
  silêncio, marcado como suspeito se parecer instrução a uma IA, e exibido só como texto simples.

## Operação
```bash
systemctl status omnira-whisper            # deve estar active (enabled no boot)
journalctl -u omnira-whisper -n 50
sudo systemctl restart omnira-whisper      # recarrega o modelo na GPU (~3 s)
docker exec omnira-worker wget -q -S -O /dev/null http://172.17.0.1:18181/   # alcance a partir do worker
docker exec omnira-worker wget -q -O - http://localhost:9090/metrics | grep transcribe
docker compose exec -T postgres psql -U omnira -d omnira_dev -c \
  "select status, count(*) from message_media_analysis group by 1;"
```
- **Motor fora do ar**: as transcrições ficam `pending` e são repetidas com recuo de 10 s até 30 min (até 30 tentativas); nada se perde
  e o áudio continua tocável. Depois disso viram `failed` ("Não foi possível transcrever").
- **GPU compartilhada** com o `llama-server` do Hermes (usa ~6,2 GB; o Whisper acrescenta ~1 GB). Fila serial: uma transcrição por vez.
- **Recompilar / trocar modelo**: `deploy/whisper/build.sh` e `fetch-model.sh` (ajuste o hash esperado). Para outro modelo, mude o
  `ExecStart` do unit e `daemon-reload`. A string do modelo gravada com cada texto está no worker (`whisper-large-v3-turbo-q5_0`).
- **Reverter**: `sudo systemctl disable --now omnira-whisper`; deixe `OMNIRA_WHISPER_URL` vazio no ambiente do worker e recrie-o: os
  áudios deixam de ser transcritos, nada mais muda.

## Desempenho medido (2026-10-04)
Áudio real de 20 s (OGG/Opus, português): idioma detectado `pt` (p = 0,9986) e ~6,6 s na primeira chamada, incluindo a partida a frio.
