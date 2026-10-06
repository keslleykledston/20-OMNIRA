DROP TABLE password_resets;
ALTER TABLE users DROP COLUMN password_expires_at;
ALTER TABLE users DROP COLUMN password_set_at;
ALTER TABLE users DROP COLUMN password_hash;
