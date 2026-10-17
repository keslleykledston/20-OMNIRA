-- ADR-0043 S4: attributes of a sector shown on the tenant screen - a short description, a colour from a closed palette and a responsible supervisor.
-- All three are informational (no routing or visibility decision reads them) and optional. The supervisor is a person (users.id); that the person is an ACTIVE member of
-- the company is checked by the API on every write and again when the sector is listed (a revoked member simply stops being shown).
SET LOCAL lock_timeout = '5s';
ALTER TABLE sectors
  ADD COLUMN description TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 280),
  ADD COLUMN color TEXT NOT NULL DEFAULT '' CHECK (color IN ('', 'slate', 'blue', 'green', 'amber', 'red', 'violet', 'teal')),
  ADD COLUMN supervisor_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
