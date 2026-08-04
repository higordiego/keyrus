DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'consolidation_app') THEN
        CREATE ROLE consolidation_app WITH LOGIN PASSWORD 'consolidation_secret';
    END IF;
END
$$;

GRANT USAGE ON SCHEMA consolidation TO consolidation_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA consolidation TO consolidation_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA consolidation TO consolidation_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA consolidation GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO consolidation_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA consolidation GRANT USAGE, SELECT ON SEQUENCES TO consolidation_app;
