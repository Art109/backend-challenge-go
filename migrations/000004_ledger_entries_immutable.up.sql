-- Enforces "append-only" at the database level: even a bug in application
-- code (or a manual UPDATE/DELETE by an operator) cannot edit or remove a
-- ledger entry. A correction is always a new entry.
CREATE FUNCTION forbid_ledger_entry_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not allowed', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_wallet_ledger_entries_no_update
    BEFORE UPDATE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_ledger_entry_mutation();

CREATE TRIGGER trg_wallet_ledger_entries_no_delete
    BEFORE DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_ledger_entry_mutation();
