# ADR-0047 — Ficha da pessoa no console do Hub: instâncias e filas de cada uma

Status: **PROPOSTA** (2026-10-14), aguarda o dono. Fase 3 do redesenho de Pessoas (Fase 1 = `a206b91`; Fase 2 = ADR-0046). Nada desta proposta está construído.

## 1. Contexto

- Pedido do dono: gerenciar uma pessoa (usuário do Hub/OMNIRA), **de quais instâncias** ela é agente e **de quais filas** de cada instância recebe conversas, num lugar só.
- Hoje isso está dividido em três telas e dois mecanismos:
  - **Membro direto de uma instância:** função, "Recebe conversas" e filas (`agent_profiles` + `queue_members`) ficam na Pessoas daquela instância, geridas pelo administrador dela.
  - **Agente do Hub:** o acesso a cada instância (Sem acesso / Só leitura / Ler e responder, "Gerenciar canais") fica em Administração › Acessos, numa tabela pessoa × instância **sem filas**; de onde recebe conversas é definido pelas **Equipes** (pools: membros com `max_open` e instâncias, com fila opcional), em outra aba.
  - Autorização por e-mail para quem ainda não tem conta (ADR-0039 §3.10), em um terceiro bloco.
- Regra do dono que continua valendo (ADR-0039): só o administrador do Hub libera alguém em mais de uma instância.

## 2. Decisão proposta

1. **Ficha da pessoa no console** (Administração › Acessos, ao clicar numa pessoa), com o mesmo padrão da ficha da instância:
   - Cabeçalho: pessoa, papel no Hub, situação (recebe conversas em N instâncias).
   - Um cartão por instância do Hub:
     - **Membro direto:** função e filas daquela instância **só para leitura**, com "Abrir na instância". A autoridade continua com o administrador da instância.
     - **Via Hub:** o acesso (já existe), "Gerenciar canais" (já existe) e **"Recebe conversas de"**: as Equipes que cobrem essa instância, com liga/desliga da pessoa em cada uma e a capacidade.
     - **Sem acesso:** "Liberar nesta instância" (grant que já existe).
2. **API mínima nova:**
   - `GET /hubs/{hub}/access/agents/{user}`: a ficha consolidada (grants, vínculos diretos com função e nomes das filas, Equipes com instâncias e fila).
   - `PUT` e `DELETE /hubs/{hub}/pools/{pool}/members/{user}`: incluir ou tirar **uma** pessoa. Hoje o `PUT .../members` substitui a lista inteira, e dois administradores ao mesmo tempo se sobrescrevem.
   - Ler a função e as filas de um membro direto de outra instância: o admin do Hub não lê `agent_profiles`/`queue_members` pela RLS. Uma função `SECURITY DEFINER hub_person_overview(hub, user)` confere `can_manage_access(hub)` e devolve só nomes de função e fila das instâncias **deste** Hub (nenhum conteúdo de conversa).
3. **Na Pessoas da instância** (Fase 1), quem atende pelo Hub aparece com o selo "via Hub", só para leitura: `GET /tenants/{t}/team/hub-agents` (nome, e-mail, acesso, Equipes que cobrem a instância), sob `membership.read`.
4. **Fora desta fase:** o admin do Hub editar as filas de membros diretos. Exigiria políticas novas em `agent_profiles`/`queue_members` com `has_hub_manage_access(..., 'people')`, além de auditoria; fica para uma decisão futura.

## 3. Segurança e provas

- Isolamento entre Hubs: o Hub X nunca vê pessoas, instâncias ou Equipes do Hub Y; instância suspensa aparece bloqueada; respostas 404 uniformes.
- Auditoria: `hub.pool_member_added` / `hub.pool_member_removed` (quem, qual Equipe, qual pessoa).
- Testes no padrão das fases do Hub: Postgres com dois Hubs e duas instâncias, suíte de mutantes (`scripts/test-hub-*-mutations.sh`), navegador real (`scripts/e2e-hub-browser.sh`), revisão do Codex.
- Sem migration se o item 4 ficar fora (a função do item 2 entra numa migration pequena).

## 4. Decisões do dono

1. A Pessoas da instância passa a mostrar quem atende nela pelo Hub (o administrador da instância vê nome e acesso dessas pessoas)?
2. O admin do Hub deve poder editar as filas de membros diretos? Recomendação: não nesta fase.
3. Ordem: Fase 2 (ADR-0046) antes desta? Recomendação: sim, porque a Fase 2 resolve o convite que hoje impede gente nova de entrar.
