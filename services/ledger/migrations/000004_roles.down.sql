REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA ledger FROM ledger_app;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA ledger FROM ledger_app;
REVOKE USAGE ON SCHEMA ledger FROM ledger_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA ledger REVOKE ALL PRIVILEGES ON SEQUENCES FROM ledger_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA ledger REVOKE ALL PRIVILEGES ON TABLES FROM ledger_app;
-- DROP OWNED BY clears every remaining privilege/default-ACL entry this role
-- holds in the CURRENT database, which is more robust than enumerating each
-- REVOKE above by hand.
--
-- ledger_app is a login role, and roles are cluster-wide in PostgreSQL: in
-- any cluster shared by more than one database that ran this migration
-- (which production never does; each service is meant to run its own
-- isolated instance, but a single shared test cluster running this suite
-- against several disposable databases does), the role can still be granted
-- on objects in a *different* database that this transaction cannot see or
-- revoke. DROP ROLE would then fail with "cannot be dropped because some
-- objects depend on it" (SQLSTATE 2BP01/dependent_objects_still_exist) even
-- though this database's own rollback is otherwise complete. The role drop
-- is therefore best-effort: attempted, and left in place with a NOTICE
-- (never a silent no-op with no explanation) if another database still
-- depends on it, instead of failing the whole rollback.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'ledger_app') THEN
        EXECUTE 'DROP OWNED BY ledger_app';
        BEGIN
            EXECUTE 'DROP ROLE ledger_app';
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'ledger_app role left in place: still granted on objects in another database sharing this cluster';
        END;
    END IF;
END
$$;
