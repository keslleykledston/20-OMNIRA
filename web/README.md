# OMNIRA Web Frontend

Interface web moderna para a plataforma SaaS OMNIRA.

## Stack

- **React 18** - UI library
- **TypeScript** - Type safety
- **Vite** - Build tool
- **TailwindCSS** - Styling
- **React Router** - Navigation
- **React Query** - Data fetching
- **Zustand** - State management
- **Axios** - HTTP client

## Desenvolvimento

```bash
# Instalar dependências
npm install

# Iniciar servidor dev
npm run dev

# Build para produção
npm run build

# Preview do build
npm run preview

# Testes
npm test

# Linting
npm run lint
```

## Estrutura

```
src/
  ├── pages/          # Páginas principais
  ├── components/     # Componentes reutilizáveis
  ├── lib/           # Utilidades e APIs
  │   ├── api.ts     # Chamadas HTTP
  │   └── store.ts   # Estado global (Zustand)
  └── App.tsx        # Componente raiz
```

## Autenticação

O frontend se conecta ao backend OMNIRA em `http://localhost:8080/api`.

Token JWT é armazenado em `localStorage` e incluído automaticamente em todas as requisições.

## Páginas

- **Login** - Autenticação
- **Dashboard** - Visão geral do sistema
- **Contas** - Gestão de contas BPO
- **Tickets** - Gestão de tickets
- **Relatórios** - Geração e exportação de relatórios

## Ambiente

```
VITE_API_URL=http://localhost:8080/api
```

## Deploy

Build estático pronto para servir com nginx ou similar:

```bash
npm run build
# ./dist contém arquivos estáticos
```
