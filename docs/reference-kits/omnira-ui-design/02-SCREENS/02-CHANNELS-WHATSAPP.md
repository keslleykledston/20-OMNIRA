# 02 CHANNELS WHATSAPP

**Rota:** `/app/channels`  
**Referência visual:** `../references/02-channels-whatsapp.png`  
**Papéis:** Admin; Supervisor somente leitura se política permitir.

## Propósito

Gerenciar conexões de canais e distinguir explicitamente WhatsApp oficial e não oficial.

## Wireframe

```text
APP SHELL
└─ PAGE
   ├─ Header + Add channel
   ├─ Channel tabs
   ├─ WhatsApp connection cards
   │  ├─ WAHA / unofficial
   │  └─ Meta Cloud / official
   └─ Informational notice
```

## Layout e regiões

- Tabs logo abaixo do header.
- Cards de conexão em coluna, largura total, ~110–130 px cada.
- Ícone do canal à esquerda, identidade e metadata no centro, badges/actions à direita.
- Notice azul-claro no rodapé da seção.

## Funcionalidades e interações

- Tabs: Todos, WhatsApp, Instagram, Facebook, Web Chat, E-mail, Telegram.
- CTA `Adicionar canal` abre seleção de provider.
- Card WAHA: Detalhes, Reconectar, Reiniciar, Desconectar, Excluir.
- Card Meta: Configurar/Detalhes/Desconectar conforme estado.
- Status e provider type sempre visíveis; nunca rotular WAHA como oficial.

## Dados necessários

ChannelConnection: id, channel, provider, providerType, displayName, phone/identifier, status, health, lastActivity, capabilities.  
Nunca incluir credential/token/session secret.

## Estados obrigatórios

Loading: skeleton cards. Empty: `Nenhum canal conectado` + CTA. Error: erro sanitizado por connection. Degraded: warning badge + ação de reconexão.

## Responsividade

Tablet mantém cards; mobile empilha metadata e move actions para menu. Tabs scrolláveis horizontalmente.

## Component map conceitual

`ChannelCard`, `ChannelBadge`, `StatusBadge`, `Tabs`, `DropdownMenu`, `InfoBanner`, `Button`.

## Checklist de fidelidade

- WAHA verde apenas no ícone; OMNIRA continua azul;
- badges `Não Oficial` e `Oficial` discretos;
- diferenças provider/status legíveis sem depender apenas de cor;
- nenhum secret visível.
