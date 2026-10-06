# Ativar Permissões de Agentes — keslley@k3gsolutions.com.br

**Problema**: Usuário `keslley@k3gsolutions.com.br` não vê `/settings/agents`

**Causa**: Permissões `agent.read` e `agent.manage` não foram concedidas

---

## Solução 1: Verificar Role Atual (Rápido)

```bash
# No navegador, abra DevTools → Console
const user = localStorage.getItem('user');
console.log(JSON.parse(user).role);
```

**Esperado**: `tenant_admin` ou `tenant_supervisor`

**Se for** `tenant_agent` ou outro: continue para Solução 2

---

## Solução 2: Atualizar Role via API (Sem UI)

Se um **admin/supervisor** atual pode executar:

```bash
# Obtenha membership_id de keslley
MEMBERSHIP_ID="<copie do /settings/team>"

# Atualize para tenant_supervisor (que tem agent.read + agent.manage)
curl -X PATCH https://seu-omnira/api/v1/tenants/{TENANT_ID}/memberships/{MEMBERSHIP_ID} \
  -H "Authorization: Bearer {SESSION_COOKIE}" \
  -H "Content-Type: application/json" \
  -d '{"role_key": "tenant_supervisor"}'
```

**Resposta esperada**: 200 OK

---

## Solução 3: SQL Direto (Se tiver acesso DB)

```sql
-- Listar membership atual
SELECT id, email, role FROM memberships WHERE email = 'keslley@k3gsolutions.com.br' LIMIT 1;

-- Atualizar para tenant_supervisor
UPDATE memberships 
SET role = 'tenant_supervisor' 
WHERE email = 'keslley@k3gsolutions.com.br';
```

---

## Roles & Permissões

| Role | Permissões | Acesso |
|------|-----------|---------|
| `tenant_admin` | Todas (inclui agent.read + agent.manage) | ✅ Vê /settings/agents |
| `tenant_supervisor` | Quase todas (inclui agent.read + agent.manage) | ✅ Vê /settings/agents |
| `tenant_agent` | Conversa (conversation.manage, etc) | ❌ NÃO vê agents |
| `tenant_member` | Leitura (conversation.read, etc) | ❌ NÃO vê agents |

---

## Verificar Acesso Após Atualizar

1. **Logout** (Settings → Logout ou limpar cookies)
2. **Login** novamente com keslley@k3gsolutions.com.br
3. **Abra** Settings → Agents
4. **Deve ver**: Lista de agentes + botão "+ Ativar novo agente"

---

## Operações Disponíveis (Após Permissão)

✅ Ver lista de agentes + presença (online/offline)  
✅ "+ Ativar novo agente" (modal com UUID)  
✅ Desativar/Ativar agente (toggle)  
✅ Gerenciar filas (adicionar, remover, ajustar capacity)  
✅ Menu ⋯ → Deletar agente (raro)  

---

## Troubleshooting

### Ainda não aparece após atualizar role?

1. **Cache do navegador**:
   ```javascript
   // DevTools → Console
   localStorage.clear();
   sessionStorage.clear();
   location.reload();
   ```

2. **Verifique permission no backend**:
   ```bash
   curl -X GET https://seu-omnira/api/v1/tenants/{TENANT_ID}/me/access \
     -H "Authorization: Bearer {TOKEN}"
   
   # Procure por "agent.read" e "agent.manage" na resposta
   ```

3. **Verifique membership_id correto**:
   ```bash
   curl -X GET https://seu-omnira/api/v1/tenants/{TENANT_ID}/team \
     -H "Authorization: Bearer {TOKEN}"
   
   # Copie o ID do usuário keslley
   ```

### Erro 403 ao chamar DELETE /agents/{id}?

- Verifique role: `tenant_supervisor` ou `tenant_admin` exigido
- Verifique permissão: deve ter `agent.manage`

---

## Atribuição Manual de Permissões (Advanced)

Se precisar de granularidade (e-mail custom com só `agent.read`, sem `agent.manage`):

Isso não é suportado pela UI atual. As permissões vêm do **role** (tenant_admin, tenant_supervisor, etc), não de uma lista individual.

Para criar um novo role com permissões customizadas, contate engenharia (requer DB migration).

---

**Próximos passos após ativar**:

1. Convidar novos agentes (Team → Convidar como `tenant_agent`)
2. Ativar em Agents (Agents → "+ Ativar novo agente")
3. Adicionar às filas (Agents → Detalhes → "Adicionar Fila")
4. Monitorar presença (green = online, gray = offline)

---

**Referências**:
- [AGENT-MANAGEMENT.md](./AGENT-MANAGEMENT.md) — Fluxo operacional
- [AGENTS-CONSOLIDATION.md](../implementation/AGENTS-CONSOLIDATION.md) — Arquitetura
- API Endpoints: `DELETE /agents/{id}` para deletar

---

**Status**: Guia para acesso de agentes (2026-10-05)
