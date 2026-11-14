-- ADR-0056: adds a free-text suggested action to each group message suggestion. Deliberately NOT a closed list —
-- the taxonomy that motivated this (docs/research/group-tratativas-ia/mapeamento-v1.md) is NOC/ISP-specific to
-- the one tenant using groups today; baking it into a CHECK would specialize a shared, multi-tenant table to one
-- tenant's domain. See ADR-0056 "Decisão" #1.
ALTER TABLE wa_group_message_suggestions
  ADD COLUMN suggested_action TEXT NOT NULL DEFAULT '';
ALTER TABLE wa_group_message_suggestions
  ADD CONSTRAINT wa_group_message_suggestions_action_len CHECK (char_length(suggested_action) <= 160);
