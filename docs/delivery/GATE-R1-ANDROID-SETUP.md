# GATE R1 — WAHA Android Real (Opção C)

**Estado**: Bloqueador de WhatsApp Web resolvido → mudança para Android real

**Por que Android?**
- GOWS (Go WhatsApp Web) sofre com rate-limiting/bloqueios do WhatsApp Web
- Android real é mais estável para produção
- Qualquer número Android pode ser usado
- Sem limite de dispositivos conectados

## Pré-requisitos

1. **Telefone Android** (real, não emulador)
   - Android 7.0+ (para suportar WhatsApp moderno)
   - WhatsApp instalado e funcional
   - Conexão USB para ADB ou SSH/rede

2. **ADB (Android Debug Bridge)** no servidor
   - `sudo apt-get install android-tools-adb`
   - Ou via docker image com ADB integrado

3. **WAHA com suporte Android**
   - Imagem: `devlikeapro/waha:gows-2026.8.2` (já presente, mas precisa de engine Android)
   - Ou: `devlikeapro/waha:android-*` (imagem específica)

## Configuração

### Passo 1: Verificar imagem WAHA Android

```bash
docker pull devlikeapro/waha:android-latest
# ou manter gows-2026.8.2 se suportar ANDROID engine
```

### Passo 2: Atualizar docker-compose.yml

```yaml
waha:
  image: devlikeapro/waha:gows-2026.8.2  # ou :android-latest
  environment:
    WHATSAPP_DEFAULT_ENGINE: ANDROID  # ← mudança
    WHATSAPP_ANDROID_DEVICE: <adb-device-id>  # ex: "emulator-5554" ou "192.168.1.100:5555"
    WAHA_API_KEY: ${WAHA_API_KEY}
    WAHA_DASHBOARD_ENABLED: "false"
    WHATSAPP_SWAGGER_ENABLED: "false"
  # ... rest
```

### Passo 3: Conectar Android ao servidor

**Via USB:**
```bash
adb devices  # lista dispositivos
adb devices -l  # mais detalhes
# Resultado esperado: "<device-id>  device"
```

**Via Network (WiFi/SSH):**
```bash
adb connect 192.168.1.100:5555
adb devices
```

### Passo 4: Ativar Modo Debug no Android

1. Configurações → Sobre do telefone
2. Pressionar "Número da compilação" 7x
3. Voltar para Configurações → Opções de desenvolvedor
4. Ativar "Depuração USB"
5. Conectar Android ao servidor via USB/WiFi

### Passo 5: Iniciar stack

```bash
export OMNIRA_WAHA_ENGINE=ANDROID
export WHATSAPP_ANDROID_DEVICE="<device-id>"
docker compose --profile whatsapp-unofficial --profile dev up -d
```

### Passo 6: Verificar saúde WAHA

```bash
docker compose logs waha | tail -50
# Esperado: "WAHA ready on port 3000" ou "Android engine initialized"
```

## Testar pareamento

1. Abrir OMNIRA web: `http://localhost:3000/inbox`
2. Login: `admin@omnira.local` / `irrelevant`
3. Ir para Canais → Adicionar WAHA
4. Aceitar risco
5. Start session → **QR gerado** (diferente de GOWS, pode ser mais rápido)
6. **No telefone Android**:
   - WhatsApp → Aparelhos conectados → Conectar aparelho
   - Escanear QR
   - Confirmar

**Diferenças vs WhatsApp Web:**
- QR pode renovar mais rapidamente
- Número é pareado como "dispositivo" no próprio Android
- Sem limite de 5 conexões simultâneas
- Sessão persiste entre restarts do Android

## Solução de problemas

| Problema | Diagnóstico |
|----------|-------------|
| WAHA não acha Android | `adb devices` retorna vazio → verificar conexão USB/rede |
| "Android engine not available" | Imagem WAHA não tem suporte Android → usar `:android-latest` |
| QR não aparece | `docker logs waha` → checar erros de inicialização |
| WhatsApp pede verificação | Normal; confirmar no Android → voltar à tela OMNIRA |
| Sessão desconecta | Android dormiu/tela apagou → ligar tela ou desativar sleep |

## Próximos passos

Quando pareamento Android funcionar:
1. ✅ GATE R1: QR + Pareamento PASS
2. → GATE R2: Inbound real de outro telefone
3. → GATE R3: Multiagent
4. → GATE R4: Outbound real
5. → GATE R5: CRM (IXC)
6. → GATE R6: RLS

## Referências

- WAHA docs: https://docs.waha.ai/en/
- Android Debug Bridge: https://developer.android.com/studio/command-line/adb
- WhatsApp multi-device support: WhatsApp Help Center

---

**Status**: Aguardando setup Android no servidor. Documentação pronta para implementação.
