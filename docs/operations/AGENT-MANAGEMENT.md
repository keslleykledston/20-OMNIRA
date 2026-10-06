# Agent Management — Criar, Ativar, Deletar

**Quick Start**: Admin → Team → Convidar como `tenant_agent` → Agents → Ativar

---

## Fluxo Completo

### 1. Criar novo membro de equipe

```
Settings → Team → "Convidar"
  Email: agente@company.com
  Função: tenant_agent
  Enviar
```

✅ Membro recebe email com link de convite + senha temporária

### 2. Membro aceita convite

```
Email → Clica link /invite/{token}
  Aceita + insere senha temporária
  Muda senha obrigatória
  Login bem-sucedido → Inbox
```

✅ User criado, membership ativa, pronto ser agente

### 3. Admin ativa como agente operacional

```
Settings → Agents → "Ativar novo agente" (botão)
  Seleciona membro: agente@company.com
  Confirma
```

✅ Agent profile criado, status=active

**API**:
```bash
POST /api/v1/tenants/{tenant_id}/agents
{
  "membership_id": "uuid-do-membro"
}
```

### 4. Admin adiciona às filas

```
Settings → Agents → Clica em agente
  "Adicionar Fila"
    Fila: Default
    Capacidade: 2
    Elegível: ✓
  Confirma
```

✅ Queue_member criado, agente elegível para roteamento

**API**:
```bash
POST /api/v1/tenants/{tenant_id}/agents/{agent_id}/queues
{
  "queue_id": "uuid-fila",
  "capacity": 2,
  "available": true
}
```

### 5. Gerenciar agente (durante operação)

#### Ativar/Desativar
```
Settings → Agents → Clica em agente
  Status: "Active" ou "Disabled"
```

**API**:
```bash
PATCH /api/v1/tenants/{tenant_id}/agents/{agent_id}
{
  "status": "active" | "disabled"
}
```

#### Ajustar elegibilidade/capacidade por fila
```
Settings → Agents → Clica em agente
  Seleciona fila
    Elegível: checkbox
    Capacidade: input
  Auto-save
```

**API**:
```bash
PATCH /api/v1/tenants/{tenant_id}/agents/{agent_id}/queues/{queue_member_id}
{
  "available": true,
  "capacity": 2
}
```

#### Remover de fila
```
Settings → Agents → Clica em agente
  Seleciona fila
  "Remover"
```

**API**:
```bash
DELETE /api/v1/tenants/{tenant_id}/agents/{agent_id}/queues/{queue_member_id}
```

### 6. Deletar agente (raro)

```
Settings → Agents → Clica em agente
  Menu ⋯ → "Deletar agente"
  Confirma
```

✅ Agent profile deletado (membership permanece)

**API**:
```bash
DELETE /api/v1/tenants/{tenant_id}/agents/{agent_id}
```

---

## Operações Comuns

### Agente em pausa/férias
```
Agents → Agente → Status: Disabled
→ Remove from all queue eligibility, can't receive assignments
```

### Agente volta do repouso
```
Agents → Agente → Status: Active
→ Automatically back to routing
```

### Rebalancear carga
```
Agents → Por agente:
  - Reduce capacity if overloaded (2 → 1)
  - Set available=false if on break
  - Add/remove from queues based on skill
```

### Monitorar presença
```
Agents page:
  - Green dot = Online (online > 2min)
  - Gray dot = Offline (idle > 10s)
  - NOT related to "available" in queue
  
"Available" (per queue) is eligibility for routing.
"Online" (presence) is human at keyboard.
```

---

## API Reference

### Authentication
All endpoints require:
- Cookie: `omnira_session` (opaque server session)
- Header: `Authorization: Bearer {token}` (legacy, dev only)

### Endpoints

| Method | Path | Permission | Açção |
|--------|------|-----------|-------|
| GET | `/agents` | agent.read | List all agents + queues |
| POST | `/agents` | agent.manage | Activate member as agent |
| GET | `/agents/{id}` | agent.read | Get one agent |
| PATCH | `/agents/{id}` | agent.manage | Enable/disable |
| **DELETE** | `/agents/{id}` | agent.manage | Delete agent |
| GET | `/agents/{id}/queues` | agent.read | List queue assignments |
| POST | `/agents/{id}/queues` | agent.manage | Add to queue |
| PATCH | `/agents/{id}/queues/{member_id}` | agent.manage | Update queue settings |
| DELETE | `/agents/{id}/queues/{member_id}` | agent.manage | Remove from queue |

### Error Codes

| Code | Meaning |
|------|---------|
| 400 | Missing/invalid field |
| 401 | Unauthorized |
| 403 | Forbidden (missing permission) |
| 404 | Agent not found |
| 409 | Conflict (already assigned, etc) |
| 500 | Server error |

---

## FAQ

**Q: Agent still offline after enabling?**
A: Presence is real-time (SSE). Reload page, or wait 10s. If still offline, agent's session may have expired — they need to log back in.

**Q: Can I delete an agent without deleting the user?**
A: Yes. DELETE `/agents/{id}` only removes the operational profile. User membership stays, so they can still be invited elsewhere or re-activated.

**Q: What happens to conversations when I disable an agent?**
A: Existing assignments stay. Agent can't receive new assignments. Admin can manually reassign to another agent via Inbox.

**Q: How do I force password reset?**
A: Go to Team → Find user → (No UI yet, use API): POST `/tenants/{id}/password-reset-request` with email, user gets reset link.

---

## Troubleshooting

### "Agent not found"
- Check tenant ID
- Verify agent is active (not deleted)
- Refresh browser

### "Permission denied"
- Confirm user has `agent.manage` permission
- User must be tenant_admin or tenant_supervisor

### "Queue member not found"
- Agent must be in that queue first
- Try adding to queue again before update

---

**Last updated**: 2026-10-05 (IAM5 Release)

Reference: `docs/implementation/AGENTS-CONSOLIDATION.md`, `web/src/lib/agents.ts`
