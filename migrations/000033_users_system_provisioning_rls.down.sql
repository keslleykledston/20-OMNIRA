-- Remove apenas o que 000033 criou. As policies users_read_self e
-- users_update_self são de 000004 e permanecem; nenhum GRANT foi concedido
-- aqui, porque o INSERT de omnira_app já vinha de 000006.
DROP POLICY IF EXISTS users_insert_system ON users;
