# ADR-0041 — Console de administração independente (agentes e empresas)

Status: **PROPOSTA** (decisão de produto do dono em 2026-10-09; nada implementado). Vocabulário de evidência: tudo abaixo é desenho (`NOT WIRED`).
Relaciona: ADR-0036 (Hub dentro do OMNIRA), ADR-0038/0039 (painel de acessos), ADR-0040 (acesso delegado).

## 1. Decisão do dono

> "Se estiver complexo a gestão, vamos usar um painel de administração independente do painel do usuário, só para gerir os agentes e empresas,
> para que a plataforma do agente seja apenas de agente/usuário."

## 2. Estado atual (o que mistura as duas coisas hoje)

Na mesma SPA do agente (`web/`), atrás de verificações de papel no menu:

| Rota | Conteúdo | Quem usa |
|---|---|---|
| `/acessos` | Agentes e permissões, Instâncias (empresas, contratos, capacidades), Equipes (rodízio), Auditoria | administrador do Hub; operador da plataforma |
| `/instancias`, `/instancias/:hub/:tenant/canais...` | Canais e integrações das instâncias, gerenciados pelo Hub | gestor delegado |

O menu lateral decide por `can_access` / `can_manage_instances`. Resultado: a plataforma do agente carrega telas, rotas e regras de exibição de administração.

## 3. O que muda e o que NÃO muda

**Sai da plataforma do agente (vai para o console):** tudo o que **configura**: pessoas/agentes e suas permissões, empresas (instâncias), contratos e o teto de capacidades (ADR-0040),
equipes e rodízio, auditoria do Hub, canais/integrações geridos pelo Hub.

**Fica na plataforma do agente:** Conversas (abas por instância), Tickets, Contatos, Grupos, Automação (uso), Dashboard e a caixa completa da instância.
Configurações **da própria instância** (Pessoas, Papéis, Canais próprios) continuam onde estão para o administrador da instância: o escopo pedido é "agentes e empresas" do Hub. Decidir depois se isso também migra.

**Não muda:** o backend (mesma API, mesmo RLS), o modelo de delegação do ADR-0040 e as fases 03 a 06. O console é uma **interface**: ele não reduz o trabalho de banco/autorização
(o agente continua lendo dados operacionais por delegação). O ganho é de superfície, isolamento e clareza.

## 4. Alternativas

- **A. Grupo de rotas separado na mesma SPA** (por exemplo `/admin/*`). Barato, mas o bundle, o cookie e a navegação continuam compartilhados: separação só visual.
- **B. Segunda entrada de front no mesmo repositório (recomendada):** `admin` com seu próprio shell, build e host (por exemplo `admin.<domínio>`), mesma API e mesmo realm de login.
  Componentes compartilhados via pasta comum. O bundle do agente deixa de conter administração; o console pode exigir mais (reautenticação, claim de MFA, lista de IPs opcional),
  ter política de CSP própria e ciclo de release independente.
- **C. Serviço e repositório separados.** Isolamento máximo, custo e divergência altos sem necessidade demonstrada.

## 5. Recomendação: B, em passos pequenos

1. Inventariar telas, rotas e chamadas de API do bloco "configura" (tabela do §2, completada) e o que é compartilhado.
2. Criar a entrada `admin` (shell, rota de login, guarda por papel de Hub/operador) reaproveitando os componentes existentes.
3. Mover as páginas; os endereços antigos redirecionam para o console por algumas versões.
4. Tirar os itens "Acessos" e "Canais das instâncias" do menu do agente (e a lógica `can_access` / `can_manage_instances` do `Sidebar`).
5. Implantar o host novo: nginx + certificado + cliente OIDC próprio no Keycloak (URIs de retorno do console). **Ação de operação, do dono/infra.**
6. Testes: Playwright de navegador real para os dois (o agente não encontra administração; o administrador não precisa do bundle do agente).

## 6. Riscos e perguntas

- **Código duplicado** entre as duas entradas: mitigar com pasta compartilhada e sem cópia de componentes.
- **Sessão entre hosts:** cookie por host (mesmo domínio pai) e CSRF/CORS conferidos; um logout em um não deve derrubar o outro sem querer.
- **Marcadores e links antigos** (`/acessos`): redirecionar.
- **Quem entra no console:** administrador do Hub e operador da plataforma. O gestor delegado de canais (agente com "Gerenciar") entra só na parte de canais? Decidir.
- **Mobile:** o console é, a princípio, desktop; o agente continua responsivo.
- **Contratos e teto de capacidades (ADR-0040):** quem os define é o operador da plataforma, com o consentimento da instância; a interface vive no console.
