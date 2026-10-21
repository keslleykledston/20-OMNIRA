-- fixture (F4): unambiguous contract statements, never allowed outside expand_then_contract_later.
ALTER TABLE widgets DROP COLUMN legacy_status;
ALTER TABLE widgets RENAME COLUMN new_status TO status;
DROP TABLE legacy_widgets;
