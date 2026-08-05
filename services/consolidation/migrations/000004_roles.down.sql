REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA consolidation FROM consolidation_app;
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA consolidation FROM consolidation_app;
REVOKE USAGE ON SCHEMA consolidation FROM consolidation_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA consolidation REVOKE ALL PRIVILEGES ON SEQUENCES FROM consolidation_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA consolidation REVOKE ALL PRIVILEGES ON TABLES FROM consolidation_app;
-- See services/ledger/migrations/000004_roles.down.sql for the full
-- rationale: DROP OWNED BY clears this database's remaining privilege/
-- default-ACL entries, and the role drop itself is best-effort because
-- roles are cluster-wide and another database sharing this cluster may
-- still depend on it.
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'consolidation_app') THEN
        EXECUTE 'DROP OWNED BY consolidation_app';
        BEGIN
            EXECUTE 'DROP ROLE consolidation_app';
        EXCEPTION WHEN dependent_objects_still_exist THEN
            RAISE NOTICE 'consolidation_app role left in place: still granted on objects in another database sharing this cluster';
        END;
    END IF;
END
$$;
