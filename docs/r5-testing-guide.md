# R5 Testing Guide: CRM Integration (Contact Sync + Activity Creation)

## Setup

### Prerequisites
- K3G CRM production token (ou ambiente de teste)
- OMNIRA API running with K3G CRM configured
- Database with conversations + contacts

### Configuration
K3G CRM credentials são armazenadas por tenant via Integrations → ERP Connections:
```
Provider: K3G CRM
Base URL: https://api.k3gsolutions.com.br
Token: production token
```

---

## R5.1: Automatic Contact Sync ✅

### Flow
```
WhatsApp Inbound Message
  ↓
FindCustomerByPhone(phone, company_id=ACME)
  ↓
Contact NOT found?
  ↓
CreateContact(name, phone, company_id=ACME)
  ↓
Store UUID in conversation.crm_contact_id
```

### Manual Test
1. Send WhatsApp message to OMNIRA number
2. Check K3G CRM:
   ```sql
   SELECT * FROM contacts WHERE phone = '+5511988887777'
   ```
   Should exist with name from WhatsApp profile

3. Verify conversation has crm_contact_id:
   ```sql
   SELECT crm_contact_id FROM conversations WHERE contact_id = '...'
   ```
   Should be a UUID

### Idempotency Test
1. Send same message again (webhook redelivery)
2. Verify NO duplicate contact is created
3. conversation.crm_contact_id should be the same UUID

---

## R5.2: Manual Activity Creation ✅

### Flow
```
Operator opens Conversation (with crm_contact_id)
  ↓
TicketPanel shows "Atividade CRM" section
  ↓
Dropdown loads companies from K3G CRM (ListCompanies)
  ↓
Operator selects company + enters subject
  ↓
POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity
  {
    "subject": "Suporte técnico para integração",
    "company_id": "uuid-of-company",
    "contact_id": "uuid-from-crm_contact_id"
  }
  ↓
Activity created in K3G CRM with type=WHATSAPP
```

### Manual Test

#### Step 1: Verify "Atividade CRM" Section Appears
1. Open OMNIRA UI
2. Navigate to Inbox → any conversation
3. Scroll to TicketPanel
4. Check if "Atividade CRM" section is visible
   - **Expected if** conversation.crm_contact_id is NOT null
   - **Not visible if** crm_contact_id is null

#### Step 2: Load Companies
1. Check browser Network tab
2. Look for request: `GET /api/v1/integrations/companies`
3. Response should be:
   ```json
   {
     "items": [
       {
         "id": "uuid-of-acme",
         "name": "ACME_TESTE",
         "cnpj": "00.000.000/0000-00"
       },
       // ... more companies
     ]
   }
   ```

#### Step 3: Create Activity
1. In TicketPanel "Atividade CRM" section:
   - Select company from dropdown (e.g., "ACME_TESTE")
   - Enter subject: "Teste de atividade R5.2"
   - Click "Criar Atividade"

2. Check Network tab for:
   ```
   POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity
   ```
   Request body:
   ```json
   {
     "subject": "Teste de atividade R5.2",
     "company_id": "uuid-of-company",
     "contact_id": "uuid-from-conversation.crm_contact_id"
   }
   ```

3. Response (201 Created):
   ```json
   {
     "id": "uuid-of-activity",
     "type": "WHATSAPP",
     "subject": "Teste de atividade R5.2",
     "contact_id": "uuid",
     "company_id": "uuid",
     "created_at": "2026-09-20T15:30:00Z"
   }
   ```

#### Step 4: Verify in K3G CRM
```sql
SELECT * FROM activities 
WHERE type = 'WHATSAPP' 
  AND subject = 'Teste de atividade R5.2'
  AND company_id = 'uuid-of-acme'
```
Should exist with timestamp matching creation time.

---

## Automated Tests

### Unit Tests (Go)
```bash
# Run only CRM tests
go test -v ./internal/tool/connectors/... -run TestK3GCRMClient
go test -v ./internal/inbox/application/... -run CRM
```

### E2E Tests (Playwright)
```bash
# Full e2e stack (creates DB, containers, runs tests)
./scripts/e2e-inbox.sh

# Or with --keep to leave stack running for debugging
./scripts/e2e-inbox.sh --keep
```

Includes test: `operador cria atividade no CRM com seleção de empresa`

---

## Common Issues

### "Atividade CRM" section doesn't appear
- **Cause**: conversation.crm_contact_id is NULL
- **Solution**: 
  1. Ensure K3G CRM is configured for the tenant
  2. Send a new WhatsApp inbound message
  3. Verify R5.1 created the contact and populated crm_contact_id

### Company dropdown is empty
- **Cause**: K3G CRM not configured or ListCompanies failed
- **Solution**:
  1. Check K3G CRM credentials in Integrations
  2. Test K3G connection: POST /api/v1/integrations/test
  3. Check API logs for CRM errors

### "POST /crm/activity" returns 400 Bad Request
- **Cause**: Missing/invalid subject, company_id, or contact_id
- **Solution**: Verify all three fields are populated before submit

### Activity created but doesn't appear in K3G CRM
- **Cause**: Network/API issue or activity was created with wrong company_id
- **Solution**:
  1. Check API response includes valid activity UUID
  2. Verify company_id matches actual company in K3G CRM
  3. Check K3G CRM logs for activity creation

---

## Code References

### Frontend (R5.2 UI)
- `web/src/components/TicketPanel.tsx` — Activity form + company dropdown
- `web/src/pages/ConversationPage.tsx` — Passes crm_contact_id to TicketPanel
- `web/e2e/ticket-panel.spec.ts` — Playwright test (R5.2 UI)

### Backend (R5.1 + R5.2)
- `internal/inbox/application/inbound.go` — Contact sync logic (R5.1)
- `internal/inbox/adapters/crm_handlers.go` — ListCompanies + CreateActivity handlers
- `internal/tool/connectors/k3gcrm.go` — K3G CRM client (FindCustomerByPhone, CreateContact, CreateActivity, ListCompanies)
- `apps/api/cmd/omnira-api/main.go` — K3G CRM wiring at startup

---

## Commits

Latest 2 commits (R5.2 implementation):
```
039f1a7 test(r5.2): e2e test para Activity Creation UI com CRM contact
0b1be3d feat(r5.2): UI para criar atividades no CRM com seleção de empresa
```

View full R5 history:
```bash
git log --oneline | grep -i "r5\|crm" | head -15
```
