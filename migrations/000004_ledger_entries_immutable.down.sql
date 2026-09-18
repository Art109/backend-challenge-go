DROP TRIGGER IF EXISTS trg_wallet_ledger_entries_no_delete ON wallet_ledger_entries;
DROP TRIGGER IF EXISTS trg_wallet_ledger_entries_no_update ON wallet_ledger_entries;
DROP FUNCTION IF EXISTS forbid_ledger_entry_mutation();
