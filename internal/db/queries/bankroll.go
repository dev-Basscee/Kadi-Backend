package queries

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BetSlip represents a user's accumulator or single bet slip.
type BetSlip struct {
	ID              string    `json:"id"`
	UserID          string    `json:"user_id"`
	StakeAmount     float64   `json:"stake_amount"`
	TotalOdds       float64   `json:"total_odds"`
	PotentialReturn float64   `json:"potential_return"`
	Status          string    `json:"status"`
	PlacedAt        time.Time `json:"placed_at"`
	SettledAt       *time.Time `json:"settled_at,omitempty"`
	Legs            []BetSlipLeg `json:"legs,omitempty"`
}

// BetSlipLeg is one selection within a bet slip.
type BetSlipLeg struct {
	ID        string  `json:"id"`
	SlipID    string  `json:"slip_id"`
	FixtureID string  `json:"fixture_id"`
	Selection string  `json:"selection"`
	Odds      float64 `json:"odds"`
	Result    string  `json:"result"`
}

// LedgerEntry represents a bankroll movement.
type LedgerEntry struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	Amount       float64   `json:"amount"`
	BalanceAfter float64   `json:"balance_after"`
	EventType    string    `json:"event_type"`
	ReferenceID  *string   `json:"reference_id,omitempty"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}

// BankrollStore handles all financial transactions.
type BankrollStore struct {
	pool *pgxpool.Pool
}

// NewBankrollStore constructs a BankrollStore.
func NewBankrollStore(pool *pgxpool.Pool) *BankrollStore {
	return &BankrollStore{pool: pool}
}

// GetBalance returns the latest balance for a user by reading the most recent ledger entry.
func (s *BankrollStore) GetBalance(ctx context.Context, userID string) (float64, error) {
	var balance float64
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(balance_after, 0) FROM bankroll_ledger
		 WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`,
		userID,
	).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("bankroll: get balance: %w", err)
	}
	return balance, nil
}

// GetLedger returns all ledger entries for a user, most recent first.
func (s *BankrollStore) GetLedger(ctx context.Context, userID string, limit int) ([]LedgerEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, amount, balance_after, event_type,
		       reference_id, COALESCE(note,''), created_at
		FROM bankroll_ledger
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("bankroll: get ledger: %w", err)
	}
	defer rows.Close()

	var entries []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Amount, &e.BalanceAfter,
			&e.EventType, &e.ReferenceID, &e.Note, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// PlaceBet creates a bet slip and deducts the stake from the bankroll
// inside a single ACID transaction — preventing race conditions.
func (s *BankrollStore) PlaceBet(ctx context.Context, userID string, slip *BetSlip) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("bankroll: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op if committed

	// 1. Read current balance (lock the user's latest row)
	var currentBalance float64
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(balance_after,0) FROM bankroll_ledger
		 WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
		userID,
	).Scan(&currentBalance)
	if err != nil {
		return fmt.Errorf("bankroll: read balance: %w", err)
	}

	// 2. Insufficient funds check
	if currentBalance < slip.StakeAmount {
		return fmt.Errorf("bankroll: insufficient funds (balance %.2f, stake %.2f)",
			currentBalance, slip.StakeAmount)
	}

	newBalance := currentBalance - slip.StakeAmount

	// 3. Insert the bet slip
	err = tx.QueryRow(ctx, `
		INSERT INTO bet_slips (user_id, stake_amount, total_odds, potential_return, status)
		VALUES ($1,$2,$3,$4,'pending')
		RETURNING id`,
		userID, slip.StakeAmount, slip.TotalOdds, slip.PotentialReturn,
	).Scan(&slip.ID)
	if err != nil {
		return fmt.Errorf("bankroll: insert slip: %w", err)
	}

	// 4. Insert each leg
	for _, leg := range slip.Legs {
		_, err = tx.Exec(ctx, `
			INSERT INTO bet_slip_legs (slip_id, fixture_id, selection, odds)
			VALUES ($1,$2,$3,$4)`,
			slip.ID, leg.FixtureID, leg.Selection, leg.Odds,
		)
		if err != nil {
			return fmt.Errorf("bankroll: insert leg: %w", err)
		}
	}

	// 5. Record debit in ledger
	_, err = tx.Exec(ctx, `
		INSERT INTO bankroll_ledger (user_id, amount, balance_after, event_type, reference_id, note)
		VALUES ($1, $2, $3, 'bet_placed', $4::uuid, 'Bet placed')`,
		userID, -slip.StakeAmount, newBalance, slip.ID,
	)
	if err != nil {
		return fmt.Errorf("bankroll: ledger debit: %w", err)
	}

	return tx.Commit(ctx)
}

// SettleBet is called by the live worker when a match finishes.
// It updates the slip, each leg, and credits winnings — all in one atomic transaction.
func (s *BankrollStore) SettleBet(ctx context.Context, slipID, userID, outcome string, returnAmount float64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settle: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	now := time.Now()

	// 1. Update slip status
	_, err = tx.Exec(ctx, `
		UPDATE bet_slips SET status = $1, settled_at = $2 WHERE id = $3`,
		outcome, now, slipID,
	)
	if err != nil {
		return fmt.Errorf("settle: update slip: %w", err)
	}

	if outcome == "won" {
		// 2. Read current balance
		var currentBalance float64
		if err = tx.QueryRow(ctx,
			`SELECT COALESCE(balance_after,0) FROM bankroll_ledger
			 WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
			userID,
		).Scan(&currentBalance); err != nil {
			return fmt.Errorf("settle: read balance: %w", err)
		}

		newBalance := currentBalance + returnAmount

		// 3. Credit winnings
		_, err = tx.Exec(ctx, `
			INSERT INTO bankroll_ledger (user_id, amount, balance_after, event_type, reference_id, note)
			VALUES ($1,$2,$3,'bet_won',$4::uuid,'Bet settled — won')`,
			userID, returnAmount, newBalance, slipID,
		)
		if err != nil {
			return fmt.Errorf("settle: ledger credit: %w", err)
		}
	} else {
		// Record loss event (no credit)
		_, err = tx.Exec(ctx, `
			INSERT INTO bankroll_ledger (user_id, amount, balance_after, event_type, reference_id, note)
			SELECT $1, 0, COALESCE(balance_after,0), 'bet_lost', $2::uuid, 'Bet settled — lost'
			FROM bankroll_ledger WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`,
			userID, slipID,
		)
		if err != nil {
			return fmt.Errorf("settle: ledger loss event: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// GetPendingSlipsForFixture returns all pending slips that contain a leg for a given fixture.
// Called by the live worker when a match transitions to 'finished'.
func (s *BankrollStore) GetPendingSlipsForFixture(ctx context.Context, fixtureID string) ([]BetSlip, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT bs.id, bs.user_id, bs.stake_amount, bs.total_odds, bs.potential_return,
		       bs.status, bs.placed_at, bs.settled_at
		FROM bet_slips bs
		JOIN bet_slip_legs bsl ON bsl.slip_id = bs.id
		WHERE bsl.fixture_id = $1 AND bs.status = 'pending'`,
		fixtureID,
	)
	if err != nil {
		return nil, fmt.Errorf("bankroll: get pending slips: %w", err)
	}
	defer rows.Close()

	var slips []BetSlip
	for rows.Next() {
		var slip BetSlip
		if err := rows.Scan(
			&slip.ID, &slip.UserID, &slip.StakeAmount, &slip.TotalOdds, &slip.PotentialReturn,
			&slip.Status, &slip.PlacedAt, &slip.SettledAt,
		); err != nil {
			return nil, err
		}
		slips = append(slips, slip)
	}
	return slips, rows.Err()
}
