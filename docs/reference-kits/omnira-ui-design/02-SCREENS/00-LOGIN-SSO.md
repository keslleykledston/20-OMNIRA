# 00 LOGIN (SSO)

**Rota:** `/login`
**Referência visual:** não há mockup externo; esta spec + o design system são a fonte.
**Papéis:** anônimo.

## Propósito

Entrar no OMNIRA. A tela deve refletir a autenticação que existe de verdade e
nada além dela.

Não há login local por senha neste produto: não existe hash, tabela de
credenciais, reset nem verificação de e-mail. Produção e staging entram
exclusivamente por OIDC/SSO. Um campo de senha aqui seria uma promessa falsa —
o operador acreditaria que a senha protege a conta quando nada a lê.

## Wireframe

```text
CANVAS
└─ CARD CENTRAL
   ├─ Logo OMNIRA
   ├─ "Entre na sua conta"
   ├─ [ Continuar com SSO ]           (primary, largura total)
   └─ texto auxiliar discreto

   (somente com dev auth ativo, abaixo do card:)
   ┌─ CALLOUT "Modo de desenvolvimento" ─┐
   │ E-mail                              │
   │ [ Entrar como usuário de teste ]    │
   └─────────────────────────────────────┘
```

## Layout e regiões

- Canvas `--color-canvas`, card central `--color-surface`, `--radius-card`,
  `--shadow-md`, largura máxima ~400px, centrado vertical e horizontalmente.
- Logo e título centralizados; ação primária ocupa a largura do card.
- O bloco de desenvolvimento é um card separado, abaixo e visualmente
  subordinado — nunca dentro do card principal.

## Estados obrigatórios

| Estado | O que a tela mostra |
|---|---|
| `default` | Logo, título, botão SSO, texto auxiliar |
| `redirecting_to_sso` | Botão em loading, desabilitado |
| `oidc_error` | Callout de erro acima da ação |
| `session_expired` | Callout informativo, tom neutro — não é erro |
| `dev_auth_enabled` | Bloco de desenvolvimento visível abaixo do card |
| `dev_auth_error` | Erro dentro do bloco de desenvolvimento |
| `unavailable` | Nenhuma forma de entrar configurada |

## Mensagens

Nunca exibir erro cru do IdP: o texto do provedor vaza detalhe de
infraestrutura e não ajuda quem está na tela.

- OIDC falhou → `Não foi possível concluir o login. Tente novamente ou contate o administrador.`
- Sessão expirada → `Sua sessão expirou. Entre novamente para continuar.`
- Sem auth configurada → `Nenhum método de autenticação está configurado neste ambiente.`

Sessão expirada é rotina, não falha: tom informativo, sem vermelho.

## Modo de desenvolvimento

Aparece apenas quando o servidor informa `dev_auth: true` em `GET /auth/mode`.
Nunca inferir de hostname, `window.location` ou query param — `?devAuth=true`
não pode habilitar nada.

O bloco pede só e-mail, porque o backend não verifica senha. A allowlist fica
no servidor; a tela não sugere nem lista os e-mails aceitos. A aparência é de
ferramenta interna (callout de aviso), não de login de produção.

Esconder este bloco é cortesia com o operador, não segurança: a rota só existe
quando o servidor a registra.

## Responsividade

Mobile (390px): card ocupa a largura com gutter de 16px, permanece centrado,
alvos de toque ≥ 44px. Sem scroll horizontal.

## Acessibilidade

- `<h1>` com o nome do produto; o card é `<main>`.
- Ação SSO é `<button>` real, alcançável por Tab, com foco visível
  (`--focus-ring`); Enter aciona.
- Callouts de erro com `role="alert"`; o de sessão expirada usa `role="status"`
  por ser informativo.
- Input de desenvolvimento com `<label>` associado.

## Component map

`Button` (primary, loading), `Input`, `Icon`, callout de estado. Tokens de
`design/tokens.css` — nenhuma cor literal e nenhuma segunda linguagem visual.

## Checklist de fidelidade

- sem campo de senha em nenhum estado;
- sem "Esqueci minha senha" e sem "Entrar com senha";
- bloco de desenvolvimento claramente separado e rotulado;
- card central sereno, sem gradiente chamativo;
- foco visível em todos os controles.
