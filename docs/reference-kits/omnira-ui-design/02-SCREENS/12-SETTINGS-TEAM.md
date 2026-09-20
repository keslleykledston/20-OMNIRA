# 12 SETTINGS TEAM

**Rota:** `/app/settings/team`  
**Referência visual:** `../references/12-settings-team.png`  
**Papéis:** Admin; Supervisor somente se permissão específica.

## Propósito

Gerenciar equipe, papéis, status e convites dentro do tenant.

## Wireframe

```text
APP SHELL
└─ SETTINGS
   ├─ Settings nav
   └─ Content
      ├─ Header + Invite user
      ├─ 4 metrics
      ├─ Search + role/status/order filters
      └─ Users table
```

## Layout e regiões

- Configurações possui subnav lateral própria (~190–210 px) dentro do conteúdo.
- Header e metrics alinhados ao restante do app.
- User rows: avatar/nome, email, role pill, status, last access, actions.

## Funcionalidades e interações

Convidar usuário, alterar role, desativar/reativar, revogar acesso.  
Evitar self-lockout: não permitir remover último admin sem processo seguro.  
Filtros Role/Status e ordenação por último acesso.

## Dados necessários

User/Membership summary: id, displayName, email, role, status, lastAccessAt. Metrics: active users, admins, supervisors, agents.

## Estados obrigatórios

Loading, no users, invitation pending, duplicate invite, permission, revoke confirmation. Mudança de role deve refletir backend imediatamente.

## Responsividade

Tablet: settings nav vira select/sidebar compacta. Mobile: nav em Sheet/segmented e user rows viram cards.

## Component map conceitual

`SettingsNav`, `MetricCard`, `SearchField`, `Select`, `UserRow`, `RoleBadge`, `StatusBadge`, `InviteUserModal`, `DropdownMenu`.

## Checklist de fidelidade

- subnav de configurações visualmente distinta da sidebar global;
- role badges suaves (Admin azul, Supervisor roxo, Agente laranja);
- status ativo verde;
- CTA convite no topo direito.
