# 03 WAHA WIZARD

**Rota:** `/app/channels/whatsapp/new?provider=waha`  
**Referência visual:** `../references/03-waha-wizard.png`  
**Papéis:** Admin.

## Propósito

Conectar uma nova sessão WhatsApp não oficial via WAHA, com QR e progressão clara.

## Wireframe

```text
APP SHELL
└─ WIZARD PAGE
   ├─ Back + title
   ├─ Stepper: 1 Configuração / 2 Conectar / 3 Escanear QR / 4 Concluído
   └─ Step content
      ├─ QR panel
      └─ Instructions/status
```

## Layout e regiões

- Conteúdo centralizado com largura ~860–980 px.
- Stepper horizontal discreto.
- No passo QR, grid 1:1: QR à esquerda, instruções à direita.
- QR em card branco com padding generoso.
- Status e expiração logo abaixo.

## Funcionalidades e interações

Passo 1: nome da conexão + provider `WAHA / Não Oficial`.  
Passo 2: cria ChannelConnection e sessão, mostra progresso.  
Passo 3: exibe QR, countdown, regeneração, polling/realtime do status.  
Passo 4: confirma número/provider/status e oferece `Ir para conversas`/`Voltar aos canais`.  
Cancelar deve confirmar se sessão temporária já existe.

## Dados necessários

connectionName, provider, session state, qrImage/qrPayload, expiresAt, phone, connection status. QR e session token são sensíveis; apenas dados necessários ao browser.

## Estados obrigatórios

Preparing, waiting_qr, qr_expired, connecting, connected, failed, cancelled. Cada estado precisa CTA coerente. Nunca spinner infinito.

## Responsividade

Mobile: stepper vira compact/numbered; QR centralizado acima das instruções; ações full-width.

## Component map conceitual

`WizardStepper`, `Card`, `QRCodeView`, `StatusBadge`, `ProgressState`, `Button`, `ErrorState`.

## Checklist de fidelidade

- step atual azul;
- QR com alto contraste e sem decoração sobre o código;
- instruções numeradas claras;
- `Não Oficial` visível no fluxo;
- transição para sucesso sem reload total.
