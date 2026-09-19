# E2E Tests - OMNIRA

Suite de testes end-to-end com **Playwright** para validação automática do frontend.

## Instalação

```bash
cd web
npm install @playwright/test
```

## Testes Inclusos

### 1. **auth.spec.ts** - Autenticação
- ✅ Login com email válido
- ✅ Rejeitar email inválido  
- ✅ Logout funciona

### 2. **dashboard.spec.ts** - Dashboard
- ✅ Exibir 4 KPI Cards
- ✅ Exibir atividades recentes
- ✅ Exibir resumo do usuário
- ✅ Navegar entre páginas

### 3. **accounts.spec.ts** - Contas BPO
- ✅ Listar contas
- ✅ Filtrar por status
- ✅ Criar nova conta
- ✅ Ver detalhes
- ✅ Suspender/Ativar conta

## Rodar Testes

### Modo Automatizado (sem UI)
```bash
npm run test:e2e
```

### Modo Interativo (com UI)
```bash
npm run test:e2e:ui
```

### Modo Headed (navegador visível)
```bash
npm run test:e2e:headed
```

### Modo Debug (passo a passo)
```bash
npm run test:e2e:debug
```

## Relatórios

Após rodar os testes, um relatório HTML é gerado:

```bash
npx playwright show-report
```

## Configuração

**arquivo**: `playwright.config.ts`

```typescript
baseURL: 'https://omnira.devops.k3gsolutions.com.br'
timeout: 30000
retries: 2 (CI)
ignoreHTTPSErrors: true (certificado auto-assinado)
```

## Credenciais de Teste

```
Email: test@omnira.local
Senha: (qualquer valor)
```

## Exemplos de Uso

### Rodar um arquivo específico
```bash
npx playwright test e2e/auth.spec.ts
```

### Rodar um teste específico
```bash
npx playwright test -g "login com email válido"
```

### Rodar com verbose
```bash
npx playwright test --reporter=list
```

## CI/CD Integration

```yaml
# GitHub Actions
- name: E2E Tests
  run: npm run test:e2e
```

## Troubleshooting

### Erro: "Unable to connect"
→ Certifique-se que a aplicação está rodando:
```bash
npm run dev  # Terminal 1
npm run test:e2e  # Terminal 2
```

### Erro: "Certificate verification failed"
→ Esperado (certificado auto-assinado). Config já tem `ignoreHTTPSErrors: true`

### Timeout
→ Aumente em `playwright.config.ts`:
```typescript
timeout: 60000  // 60 segundos
```

## Estrutura de Testes

```
e2e/
├── auth.spec.ts        # Testes de autenticação
├── dashboard.spec.ts   # Testes de dashboard
├── accounts.spec.ts    # Testes de contas
└── playwright.config.ts # Configuração

playwright.config.ts    # Config principal (na raiz de web/)
```

## Próximos Passos

- [ ] Adicionar testes para Tickets
- [ ] Adicionar testes para Relatórios
- [ ] Adicionar visual regression tests
- [ ] Integrar com CI/CD
- [ ] Performance tests

---

**Status**: ✅ Pronto para usar
**Coverage**: 3 suites, 14 testes
