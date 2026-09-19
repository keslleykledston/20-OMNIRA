# CONTEXT — Vocabulário canônico do OMNIRA

Este arquivo contém **termos de domínio**, não detalhes de implementação.

## Tenant

Empresa contratante isolada logicamente na plataforma. No contexto comercial brasileiro, normalmente corresponde a um CNPJ, mas o identificador técnico não deve ser o CNPJ.

**Evitar:** usar “cliente” para significar Tenant e também consumidor final.

## Hub BPO

Agrupador operacional que concede a uma organização terceirizada acesso controlado a um conjunto de Tenants. O Hub não é proprietário dos dados dos Tenants.

## Organização Operadora

Empresa que executa atendimento para terceiros por meio de um Hub BPO.

## Usuário

Identidade humana autenticada.

## Membership

Relação entre Usuário e Tenant ou entre Usuário e Hub, contendo papel e permissões.

## TenantContext

Contexto explícito e imutável aplicado a cada operação de domínio que acessa dados de um Tenant.

## Conversa

Thread omnichannel com um contato em um canal. Pode originar ou estar associada a um Ticket.

## Ticket

Unidade de trabalho operacional com estado, fila, prioridade, SLA e responsável.

## Contato

Pessoa física ou entidade externa atendida pelo Tenant. Um mesmo ser humano presente em dois Tenants produz dois registros independentes.

## Canal

Origem/destino de mensagens: WhatsApp, Webchat, Instagram, Telegram, E-mail etc.

## Conexão de Canal

Credencial/configuração de um Canal pertencente exclusivamente a um Tenant.

## Fila

Conjunto ordenado de Tickets aguardando roteamento ou atendimento.

## Departamento

Agrupamento operacional dentro de um Tenant.

## Skill de Atendimento

Capacidade atribuída a operadores e exigida por filas/regras de roteamento. Não confundir com “Agent Skill” de automação do repositório.

## Operador

Usuário que atende Tickets. Pode ser interno ao Tenant ou terceirizado via Hub BPO.

## Integração ERP

Adapter do Tenant para um sistema externo, como IXC, SGP ou Hubsoft.

## Ação de Self-Service

Operação automatizada iniciada por chatbot ou operador, como segunda via, desbloqueio em confiança ou abertura de chamado.

## Evento de Domínio

Fato imutável ocorrido no domínio, usado para integrações, auditoria e automações.

## Auditoria

Registro append-only de ações relevantes à segurança, acesso e alteração de estado.

## Isolation Tier

Política de persistência e isolamento de dados atribuída a um Tenant.

### Shared Strong Isolation
Modelo padrão do SaaS: infraestrutura de banco compartilhada, `tenant_id` obrigatório, enforcement por aplicação + RLS/controle equivalente, auditoria e testes adversariais.

### Dedicated Database
Tier Enterprise/BPO: banco dedicado ao Tenant (ou conjunto contratualmente definido), mantendo os mesmos contratos de domínio e autorização da aplicação.

O tier de isolamento é uma propriedade de implantação/persistência e não altera a semântica de Tenant, Hub, Ticket, Conversa ou autorização.
