-- JIT provisioning de identidade OIDC sob FORCE RLS.
--
-- 000004 deu a users apenas users_read_self e users_update_self. Sem policy de
-- INSERT, o primeiro login de um usuário novo falha: ProvisionIdentity abre uma
-- sessão de sistema e tenta inserir a linha, mas FORCE RLS nega a operação.
--
-- A policy é a mais estreita possível: só o contexto de sistema insere. Para um
-- INSERT não existe "self" (a linha ainda não existe), então current_user_id()
-- não tem papel aqui.
--
-- app.is_system_admin nunca vem do request: em todos os call sites o flag é um
-- literal no código Go, e os caminhos que servem request autenticado passam
-- false. Não é o mesmo que tenant_admin — esse é um papel de membership,
-- avaliado por has_active_membership(), sem relação com este GUC.
--
-- omnira_app já tem INSERT em users desde 000006, então nenhum GRANT novo é
-- necessário: quem barrava a operação era a ausência da policy.

CREATE POLICY users_insert_system ON users FOR INSERT
  WITH CHECK (is_system_admin());
