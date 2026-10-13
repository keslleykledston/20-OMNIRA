-- DATA LOSS: drops every agent shift, on-call roster and period. Rolling back the code does not need it (the old code ignores these tables).
DROP TABLE IF EXISTS on_call_periods;
DROP TABLE IF EXISTS on_call_rosters;
DROP TABLE IF EXISTS agent_shifts;
