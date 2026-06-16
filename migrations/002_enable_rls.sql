-- 002_enable_rls.sql — close the Supabase Data API surface on every table.
--
-- WHY: Supabase auto-exposes all `public` tables over its PostgREST Data
-- API, reachable with the project's PUBLIC anon key. With RLS disabled,
-- anyone holding that key could read (and depending on grants, write) the
-- entire odds-history database — the moat. Enabling RLS with NO policies
-- flips PostgREST to default-deny for the anon/authenticated roles.
--
-- This does NOT affect corridord: it connects as the table-owner role over
-- the Postgres wire protocol, and owners BYPASS RLS. Same for local dev
-- (the `corridor` owner role). Ingestion is untouched — only the public
-- REST surface is closed. We never use PostgREST; Phase 4's web page will
-- read corridord's own /v1 API, not Supabase directly.

-- +goose Up
ALTER TABLE venues         ENABLE ROW LEVEL SECURITY;
ALTER TABLE events         ENABLE ROW LEVEL SECURITY;
ALTER TABLE markets        ENABLE ROW LEVEL SECURITY;
ALTER TABLE outcomes       ENABLE ROW LEVEL SECURITY;
ALTER TABLE quotes         ENABLE ROW LEVEL SECURITY;
ALTER TABLE market_matches ENABLE ROW LEVEL SECURITY;
ALTER TABLE fx_rates       ENABLE ROW LEVEL SECURITY;
ALTER TABLE alerts         ENABLE ROW LEVEL SECURITY;

-- +goose Down
ALTER TABLE alerts         DISABLE ROW LEVEL SECURITY;
ALTER TABLE fx_rates       DISABLE ROW LEVEL SECURITY;
ALTER TABLE market_matches DISABLE ROW LEVEL SECURITY;
ALTER TABLE quotes         DISABLE ROW LEVEL SECURITY;
ALTER TABLE outcomes       DISABLE ROW LEVEL SECURITY;
ALTER TABLE markets        DISABLE ROW LEVEL SECURITY;
ALTER TABLE events         DISABLE ROW LEVEL SECURITY;
ALTER TABLE venues         DISABLE ROW LEVEL SECURITY;
