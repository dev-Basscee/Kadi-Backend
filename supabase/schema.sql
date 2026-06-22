-- ============================================================
-- Kadi — Supabase PostgreSQL Schema
-- Run this in the Supabase SQL Editor to bootstrap all tables.
-- ============================================================

-- Enable UUID generation extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ─── Profiles ────────────────────────────────────────────────
-- Mirrors auth.users; extended with app-specific fields.
CREATE TABLE IF NOT EXISTS profiles (
    id            UUID PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
    username      TEXT UNIQUE,
    avatar_url    TEXT,
    is_premium    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── Sports & Leagues ────────────────────────────────────────
CREATE TABLE IF NOT EXISTS leagues (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    sport       TEXT NOT NULL CHECK (sport IN ('football','basketball','tennis','cricket')),
    country     TEXT,
    logo_url    TEXT,
    api_id      TEXT UNIQUE   -- external API identifier (e.g., API-Football league ID)
);

-- ─── Teams ───────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS teams (
    id          SERIAL PRIMARY KEY,
    name        TEXT NOT NULL,
    short_name  TEXT,
    logo_url    TEXT,
    league_id   INT REFERENCES leagues(id),
    api_id      TEXT UNIQUE
);

-- ─── Fixtures (Matches) ───────────────────────────────────────
CREATE TABLE IF NOT EXISTS fixtures (
    id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    api_id              TEXT UNIQUE,   -- external match ID for idempotent upserts
    sport               TEXT NOT NULL CHECK (sport IN ('football','basketball','tennis','cricket')),
    league_id           INT REFERENCES leagues(id),
    home_team_id        INT REFERENCES teams(id),
    away_team_id        INT REFERENCES teams(id),
    home_team_name      TEXT NOT NULL,
    away_team_name      TEXT NOT NULL,
    home_team_logo      TEXT,
    away_team_logo      TEXT,
    match_date          TIMESTAMPTZ NOT NULL,
    match_time          TEXT,
    status              TEXT NOT NULL DEFAULT 'upcoming' CHECK (status IN ('upcoming','live','finished','postponed')),
    home_score          INT,
    away_score          INT,
    minute              INT,
    odds_home           NUMERIC(6,2),
    odds_draw           NUMERIC(6,2),
    odds_away           NUMERIC(6,2),
    probability_home    NUMERIC(5,2),
    probability_draw    NUMERIC(5,2),
    probability_away    NUMERIC(5,2),
    home_form           INT[],         -- last 5 performance scores 0-100
    away_form           INT[],
    h2h_home_wins       INT DEFAULT 0,
    h2h_away_wins       INT DEFAULT 0,
    h2h_draws           INT DEFAULT 0,
    is_premium          BOOLEAN DEFAULT FALSE,
    ingested_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Auto-update updated_at on every row change
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN NEW.updated_at = NOW(); RETURN NEW; END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER fixtures_updated_at
BEFORE UPDATE ON fixtures
FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- ─── AI Predictions ──────────────────────────────────────────
CREATE TABLE IF NOT EXISTS predictions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    fixture_id      UUID NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
    result          TEXT NOT NULL CHECK (result IN ('win','loss','draw')),
    confidence      INT NOT NULL CHECK (confidence BETWEEN 0 AND 100),
    reasoning       TEXT,        -- AI-generated narrative
    model_version   TEXT,        -- e.g., "gemini-1.5-flash"
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (fixture_id)          -- one prediction per fixture
);

-- ─── Injury & Lineup Updates ─────────────────────────────────
CREATE TABLE IF NOT EXISTS injury_updates (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    fixture_id  UUID REFERENCES fixtures(id) ON DELETE CASCADE,
    team_id     INT REFERENCES teams(id),
    player_name TEXT NOT NULL,
    status      TEXT NOT NULL CHECK (status IN ('injured','doubtful','suspended','available')),
    note        TEXT,
    reported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── Bankroll Ledger ─────────────────────────────────────────
-- Each row represents one credit or debit event for a user.
CREATE TABLE IF NOT EXISTS bankroll_ledger (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    amount          NUMERIC(12,2) NOT NULL, -- positive = credit, negative = debit
    balance_after   NUMERIC(12,2) NOT NULL,
    event_type      TEXT NOT NULL CHECK (event_type IN ('deposit','withdrawal','bet_placed','bet_won','bet_lost','bonus')),
    reference_id    UUID,         -- optional: linked bet_slip id
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── Bet Slips ───────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS bet_slips (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    stake_amount    NUMERIC(10,2) NOT NULL,
    total_odds      NUMERIC(10,4) NOT NULL,
    potential_return NUMERIC(12,2),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','won','lost','void','cashout')),
    placed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at      TIMESTAMPTZ
);

-- Individual legs within an accumulator slip
CREATE TABLE IF NOT EXISTS bet_slip_legs (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    slip_id     UUID NOT NULL REFERENCES bet_slips(id) ON DELETE CASCADE,
    fixture_id  UUID NOT NULL REFERENCES fixtures(id),
    selection   TEXT NOT NULL,   -- e.g., "home_win", "draw", "away_win"
    odds        NUMERIC(6,2) NOT NULL,
    result      TEXT CHECK (result IN ('won','lost','void','pending')) DEFAULT 'pending'
);

-- ─── Watchlist ───────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS watchlist (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    fixture_id  UUID NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
    added_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, fixture_id)
);

-- ─── Community Posts ─────────────────────────────────────────
CREATE TABLE IF NOT EXISTS community_posts (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    fixture_id  UUID REFERENCES fixtures(id),
    content     TEXT NOT NULL,
    upvotes     INT DEFAULT 0,
    is_pinned   BOOLEAN DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── Row Level Security (RLS) ────────────────────────────────
-- Enable RLS on user-facing tables so Supabase enforces ownership.
ALTER TABLE profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE bankroll_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE bet_slips ENABLE ROW LEVEL SECURITY;
ALTER TABLE bet_slip_legs ENABLE ROW LEVEL SECURITY;
ALTER TABLE watchlist ENABLE ROW LEVEL SECURITY;
ALTER TABLE community_posts ENABLE ROW LEVEL SECURITY;

-- Profiles: user can only see/edit their own
CREATE POLICY "profiles_self" ON profiles FOR ALL USING (auth.uid() = id);

-- Bankroll: user sees only their ledger rows
CREATE POLICY "ledger_self" ON bankroll_ledger FOR ALL USING (auth.uid() = user_id);

-- Bet slips
CREATE POLICY "slips_self" ON bet_slips FOR ALL USING (auth.uid() = user_id);
CREATE POLICY "slip_legs_self" ON bet_slip_legs FOR ALL
  USING (slip_id IN (SELECT id FROM bet_slips WHERE user_id = auth.uid()));

-- Watchlist
CREATE POLICY "watchlist_self" ON watchlist FOR ALL USING (auth.uid() = user_id);

-- Community: readable by all, writable only by owner
CREATE POLICY "community_read" ON community_posts FOR SELECT USING (TRUE);
CREATE POLICY "community_write" ON community_posts FOR INSERT WITH CHECK (auth.uid() = user_id);
CREATE POLICY "community_update" ON community_posts FOR UPDATE USING (auth.uid() = user_id);

-- ─── Indexes ─────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_fixtures_status   ON fixtures(status);
CREATE INDEX IF NOT EXISTS idx_fixtures_date     ON fixtures(match_date);
CREATE INDEX IF NOT EXISTS idx_fixtures_sport    ON fixtures(sport);
CREATE INDEX IF NOT EXISTS idx_ledger_user       ON bankroll_ledger(user_id);
CREATE INDEX IF NOT EXISTS idx_slips_user        ON bet_slips(user_id);
CREATE INDEX IF NOT EXISTS idx_watchlist_user    ON watchlist(user_id);

-- Enable Supabase Realtime on fixtures so Go worker updates propagate instantly
ALTER PUBLICATION supabase_realtime ADD TABLE fixtures;
ALTER PUBLICATION supabase_realtime ADD TABLE injury_updates;
