package queries

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture represents a row from the fixtures table.
type Fixture struct {
	ID              string    `json:"id"`
	ApiID           string    `json:"api_id"`
	Sport           string    `json:"sport"`
	HomeTeamName    string    `json:"home_team_name"`
	AwayTeamName    string    `json:"away_team_name"`
	HomeTeamLogo    string    `json:"home_team_logo"`
	AwayTeamLogo    string    `json:"away_team_logo"`
	MatchDate       time.Time `json:"match_date"`
	MatchTime       string    `json:"match_time"`
	Status          string    `json:"status"`
	HomeScore       *int      `json:"home_score"`
	AwayScore       *int      `json:"away_score"`
	Minute          *int      `json:"minute"`
	OddsHome        float64   `json:"odds_home"`
	OddsDraw        float64   `json:"odds_draw"`
	OddsAway        float64   `json:"odds_away"`
	ProbabilityHome float64   `json:"probability_home"`
	ProbabilityDraw float64   `json:"probability_draw"`
	ProbabilityAway float64   `json:"probability_away"`
	HomeForm        []int     `json:"home_form"`
	AwayForm        []int     `json:"away_form"`
	H2HHomeWins     int       `json:"h2h_home_wins"`
	H2HAwayWins     int       `json:"h2h_away_wins"`
	H2HDraws        int       `json:"h2h_draws"`
	IsPremium       bool      `json:"is_premium"`
	TxlineSignature *string   `json:"txline_signature"`
	MerkleRoot      *string   `json:"merkle_root"`
	ProofReceipt    []byte    `json:"proof_receipt"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// FixtureStore provides database access for fixtures.
type FixtureStore struct {
	pool *pgxpool.Pool
}

// NewFixtureStore constructs a FixtureStore.
func NewFixtureStore(pool *pgxpool.Pool) *FixtureStore {
	return &FixtureStore{pool: pool}
}

// ListByDate returns all fixtures for a given date, ordered by match time.
func (s *FixtureStore) ListByDate(ctx context.Context, date time.Time) ([]Fixture, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			id, COALESCE(api_id,''), sport,
			home_team_name, away_team_name,
			COALESCE(home_team_logo,''), COALESCE(away_team_logo,''),
			match_date, COALESCE(match_time,''),
			status, home_score, away_score, minute,
			COALESCE(odds_home,0), COALESCE(odds_draw,0), COALESCE(odds_away,0),
			COALESCE(probability_home,0), COALESCE(probability_draw,0), COALESCE(probability_away,0),
			COALESCE(home_form,'{}'), COALESCE(away_form,'{}'),
			h2h_home_wins, h2h_away_wins, h2h_draws,
			is_premium, txline_signature, merkle_root, proof_receipt, updated_at
		FROM fixtures
		WHERE match_date::date = $1::date
		ORDER BY match_date ASC`,
		date,
	)
	if err != nil {
		return nil, fmt.Errorf("fixtures: list by date: %w", err)
	}
	defer rows.Close()

	return collectFixtures(rows)
}

// ListLive returns all currently live fixtures.
func (s *FixtureStore) ListLive(ctx context.Context) ([]Fixture, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			id, COALESCE(api_id,''), sport,
			home_team_name, away_team_name,
			COALESCE(home_team_logo,''), COALESCE(away_team_logo,''),
			match_date, COALESCE(match_time,''),
			status, home_score, away_score, minute,
			COALESCE(odds_home,0), COALESCE(odds_draw,0), COALESCE(odds_away,0),
			COALESCE(probability_home,0), COALESCE(probability_draw,0), COALESCE(probability_away,0),
			COALESCE(home_form,'{}'), COALESCE(away_form,'{}'),
			h2h_home_wins, h2h_away_wins, h2h_draws,
			is_premium, txline_signature, merkle_root, proof_receipt, updated_at
		FROM fixtures
		WHERE status = 'live'
		ORDER BY match_date ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("fixtures: list live: %w", err)
	}
	defer rows.Close()

	return collectFixtures(rows)
}

// GetByID fetches a single fixture by its UUID.
func (s *FixtureStore) GetByID(ctx context.Context, id string) (*Fixture, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT
			id, COALESCE(api_id,''), sport,
			home_team_name, away_team_name,
			COALESCE(home_team_logo,''), COALESCE(away_team_logo,''),
			match_date, COALESCE(match_time,''),
			status, home_score, away_score, minute,
			COALESCE(odds_home,0), COALESCE(odds_draw,0), COALESCE(odds_away,0),
			COALESCE(probability_home,0), COALESCE(probability_draw,0), COALESCE(probability_away,0),
			COALESCE(home_form,'{}'), COALESCE(away_form,'{}'),
			h2h_home_wins, h2h_away_wins, h2h_draws,
			is_premium, txline_signature, merkle_root, proof_receipt, updated_at
		FROM fixtures WHERE id = $1`, id)

	var f Fixture
	err := scanFixture(row, &f)
	if err != nil {
		return nil, fmt.Errorf("fixtures: get by id %s: %w", id, err)
	}
	return &f, nil
}

// UpsertFixture inserts or updates a fixture using the external api_id as conflict key.
// This makes ingestion workers idempotent — safe to call repeatedly.
func (s *FixtureStore) UpsertFixture(ctx context.Context, f *Fixture) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fixtures (
			api_id, sport, home_team_name, away_team_name,
			home_team_logo, away_team_logo,
			match_date, match_time, status,
			home_score, away_score, minute,
			odds_home, odds_draw, odds_away,
			probability_home, probability_draw, probability_away,
			home_form, away_form,
			h2h_home_wins, h2h_away_wins, h2h_draws,
			is_premium, txline_signature, merkle_root, proof_receipt
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
			$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,
			$25,$26,$27
		)
		ON CONFLICT (api_id) DO UPDATE SET
			status          = EXCLUDED.status,
			home_score      = EXCLUDED.home_score,
			away_score      = EXCLUDED.away_score,
			minute          = EXCLUDED.minute,
			odds_home       = EXCLUDED.odds_home,
			odds_draw       = EXCLUDED.odds_draw,
			odds_away       = EXCLUDED.odds_away,
			txline_signature= EXCLUDED.txline_signature,
			merkle_root     = EXCLUDED.merkle_root,
			proof_receipt   = EXCLUDED.proof_receipt,
			updated_at      = NOW()`,
		f.ApiID, f.Sport, f.HomeTeamName, f.AwayTeamName,
		f.HomeTeamLogo, f.AwayTeamLogo,
		f.MatchDate, f.MatchTime, f.Status,
		f.HomeScore, f.AwayScore, f.Minute,
		f.OddsHome, f.OddsDraw, f.OddsAway,
		f.ProbabilityHome, f.ProbabilityDraw, f.ProbabilityAway,
		f.HomeForm, f.AwayForm,
		f.H2HHomeWins, f.H2HAwayWins, f.H2HDraws,
		f.IsPremium,
		f.TxlineSignature, f.MerkleRoot, f.ProofReceipt,
	)
	return err
}

// UpdateLiveScore atomically updates score and match status for a live fixture.
func (s *FixtureStore) UpdateLiveScore(ctx context.Context, apiID, status string, homeScore, awayScore, minute int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fixtures
		SET status = $2, home_score = $3, away_score = $4, minute = $5, updated_at = NOW()
		WHERE api_id = $1`,
		apiID, status, homeScore, awayScore, minute,
	)
	return err
}

// UpdateLiveScoreWithTxLine atomically updates score, match status, and TxLine proofs for a live fixture.
func (s *FixtureStore) UpdateLiveScoreWithTxLine(ctx context.Context, apiID, status string, homeScore, awayScore, minute int, sig, merkle string, proof []byte) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fixtures
		SET status = $2, home_score = $3, away_score = $4, minute = $5,
		    txline_signature = $6, merkle_root = $7, proof_receipt = $8, updated_at = NOW()
		WHERE api_id = $1`,
		apiID, status, homeScore, awayScore, minute, sig, merkle, proof,
	)
	return err
}

// UpdateOdds atomically updates the 1X2 odds for a fixture.
func (s *FixtureStore) UpdateOdds(ctx context.Context, apiID string, home, draw, away float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fixtures
		SET odds_home = $2, odds_draw = $3, odds_away = $4, updated_at = NOW()
		WHERE api_id = $1`,
		apiID, home, draw, away,
	)
	return err
}

// ─── helpers ────────────────────────────────────────────────────────────────

type scannable interface {
	Scan(dest ...any) error
}

func scanFixture(row scannable, f *Fixture) error {
	return row.Scan(
		&f.ID, &f.ApiID, &f.Sport,
		&f.HomeTeamName, &f.AwayTeamName,
		&f.HomeTeamLogo, &f.AwayTeamLogo,
		&f.MatchDate, &f.MatchTime,
		&f.Status, &f.HomeScore, &f.AwayScore, &f.Minute,
		&f.OddsHome, &f.OddsDraw, &f.OddsAway,
		&f.ProbabilityHome, &f.ProbabilityDraw, &f.ProbabilityAway,
		&f.HomeForm, &f.AwayForm,
		&f.H2HHomeWins, &f.H2HAwayWins, &f.H2HDraws,
		&f.IsPremium, &f.TxlineSignature, &f.MerkleRoot, &f.ProofReceipt, &f.UpdatedAt,
	)
}

func collectFixtures(rows pgx.Rows) ([]Fixture, error) {
	var fixtures []Fixture
	for rows.Next() {
		var f Fixture
		if err := scanFixture(rows, &f); err != nil {
			return nil, err
		}
		fixtures = append(fixtures, f)
	}
	return fixtures, rows.Err()
}
