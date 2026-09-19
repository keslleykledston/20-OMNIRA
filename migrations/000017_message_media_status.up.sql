-- M03.3: canonical media metadata retained without provider payload leakage.
ALTER TABLE messages ADD COLUMN media_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN mime_type TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0);
