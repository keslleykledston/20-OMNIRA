# R1.0 Frontend - OMNIRA

**Status**: ✅ Estrutura criada, pronto para desenvolvimento

## Início Rápido

```bash
# 1. Navegar para diretório web
cd web

# 2. Instalar dependências
npm install

# 3. Iniciar servidor de desenvolvimento
npm run dev

# 4. Abrir http://localhost:3000 no navegador
```

## O que foi criado

### Estrutura Base
- ✅ **Configuração Vite** com React 18 + TypeScript
- ✅ **TailwindCSS** para styling
- ✅ **React Router** para navegação
- ✅ **React Query** para data fetching
- ✅ **Zustand** para state management
- ✅ **Axios** com interceptadores para autenticação

### Componentes
- ✅ **Layout** - Container principal com Sidebar + Header
- ✅ **Sidebar** - Navegação entre seções
- ✅ **Header** - Informações de usuário e logout

### Páginas
- ✅ **Login** - Tela de autenticação
- ✅ **Dashboard** - Visão geral com métricas
- ✅ **Accounts** - Lista e criação de contas BPO
- ✅ **Tickets** - Gestão de tickets com status
- ✅ **Reports** - Exportação de relatórios

### Utilitários
- ✅ **lib/api.ts** - Clientes API para todos os endpoints
- ✅ **lib/store.ts** - Estado global (auth + UI)
- ✅ **lib/types.ts** - Tipos TypeScript alinhados com backend

## Próximos Passos (T38-T45)

### T38: Integração de Autenticação Completa
- [ ] Testar login contra backend real
- [ ] Validar JWT refresh
- [ ] Implementar logout automático
- [ ] Guardar session no localStorage

### T39: Dashboard com Dados Reais
- [ ] Integrar API de métricas
- [ ] Gráficos com Chart.js/Recharts
- [ ] Feed de atividades em tempo real
- [ ] Alerts e notificações

### T40: Contas BPO - Funcionalidade Completa
- [ ] Criar/editar/excluir contas
- [ ] Filtros e busca
- [ ] SLA configuration
- [ ] Métricas por conta

### T41: Tickets - CRUD e Workflows
- [ ] Criar/editar/resolver tickets
- [ ] Atribição de tickets
- [ ] Histórico de alterações
- [ ] Priorização

### T42: Relatórios - Geração e Exportação
- [ ] Interface de customização
- [ ] Geração em tempo real
- [ ] Suporte a múltiplos formatos
- [ ] Agendamento de exportação

### T43: Supervisor Dashboard
- [ ] Visão multi-conta
- [ ] Alerts agregados
- [ ] Controle de SLA
- [ ] Performance metrics

### T44: Testes & Performance
- [ ] Testes com Vitest
- [ ] Lighthouse audit
- [ ] Code splitting
- [ ] Lazy loading

### T45: Build & Deploy
- [ ] Build estático otimizado
- [ ] Docker image para frontend
- [ ] Nginx reverse proxy config
- [ ] Cache busting e versioning

## Variáveis de Ambiente

Criar arquivo `.env` na raiz do diretório `web/`:

```env
VITE_API_URL=http://localhost:8080/api
VITE_APP_TITLE=OMNIRA
```

## Desenvolvimento Local

O Vite proxy redirecionará automaticamente `/api/*` para `http://localhost:8080/api/*`.

**Backend deve estar rodando**:
```bash
cd ..  # volta para diretório raiz
go run cmd/api/main.go
```

## Estrutura de Arquivos

```
web/
├── index.html              # Entrypoint HTML
├── package.json           # Dependências
├── tsconfig.json          # Configuração TypeScript
├── vite.config.ts         # Configuração Vite
├── tailwind.config.js     # TailwindCSS
├── postcss.config.js      # PostCSS
├── src/
│   ├── main.tsx          # Inicialização
│   ├── App.tsx           # Rota principal
│   ├── index.css         # Estilos globais
│   ├── pages/
│   │   ├── Login.tsx
│   │   ├── Dashboard.tsx
│   │   ├── Accounts.tsx
│   │   ├── Tickets.tsx
│   │   └── Reports.tsx
│   ├── components/
│   │   ├── Layout.tsx
│   │   ├── Header.tsx
│   │   └── Sidebar.tsx
│   └── lib/
│       ├── api.ts        # Clientes HTTP
│       ├── store.ts      # Estado Zustand
│       └── types.ts      # Tipos compartilhados
└── README.md
```

## Notas Importantes

1. **Autenticação**: Token é enviado automaticamente via header `Authorization: Bearer <token>`
2. **Tenant Isolation**: Frontend segue a mesma isolação de tenant do backend
3. **CORS**: Backend já está configurado para aceitar requisições do frontend em `http://localhost:3000`
4. **Erro 401**: Logout automático se token expirar ou for inválido
5. **Tipos**: TypeScript com strict mode ativo para máxima segurança

## Verificação de Status

Ao iniciar `npm run dev`, você deve ver:

```
  VITE v5.0.0  ready in 123 ms

  ➜  Local:   http://localhost:3000/
  ➜  press h to show help
```

Acesse `http://localhost:3000` → Deve redirecionar para `/login`

---

**Próximo Comando**: `prossiga` para iniciar T38 (Integração de Autenticação Completa)
