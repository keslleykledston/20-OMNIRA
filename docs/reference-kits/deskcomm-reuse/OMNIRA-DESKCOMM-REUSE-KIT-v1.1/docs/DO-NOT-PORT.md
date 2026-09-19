# Do Not Port

Não incorporar como decisão arquitetural do OMNIRA:

- backend Next.js Route Handlers;
- Supabase Auth obrigatório;
- Supabase Realtime obrigatório;
- Supabase Storage obrigatório;
- service-role bypass patterns;
- event_log como substituto do NATS;
- cron como mecanismo principal de worker;
- Caddy;
- WAHA como canal principal;
- Upstash como requisito;
- Sentry como telemetry foundation;
- Vercel AI Gateway como requisito de plataforma;
- Nuvemshop no MVP;
- branding Deskcomm.

Pode estudar os edge cases, mas não carregar a dependência.

## Exceção controlada — WhatsApp não oficial

Provedores não oficiais podem existir como **adapters opcionais** quando o Tenant explicitamente optar por esse modo.

Eles não podem:
- substituir o contrato canônico de `ChannelProvider`;
- contaminar o domínio com sessão/QR/browser;
- ser requisito para o funcionamento do OMNIRA;
- compartilhar sessão/credencial entre Tenants;
- receber status de "oficial" na UI;
- ocultar riscos operacionais/compliance do Tenant.
