package domain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"dispatch/agentkit"
)

// ErrNotFound is returned when a domain entity does not exist.
var ErrNotFound = errors.New("domain: not found")

// Store persists the dispatch domain in Postgres.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// --- customers ---

// GetOrCreateCustomer finds a customer by phone or creates one.
func (s *Store) GetOrCreateCustomer(ctx context.Context, orgID, phone, name string) (*Customer, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO customers (id, org_id, phone, name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, phone) DO UPDATE
		SET name = CASE WHEN customers.name = '' THEN EXCLUDED.name ELSE customers.name END`,
		agentkit.NewID(), orgID, phone, name)
	if err != nil {
		return nil, err
	}
	var c Customer
	err = s.pool.QueryRow(ctx, `
		SELECT id, org_id, phone, name, created_at FROM customers
		WHERE org_id = $1 AND phone = $2`, orgID, phone).
		Scan(&c.ID, &c.OrgID, &c.Phone, &c.Name, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	var c Customer
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, phone, name, created_at FROM customers WHERE id = $1`, id).
		Scan(&c.ID, &c.OrgID, &c.Phone, &c.Name, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// --- channel connections ---

const channelConnectionSelect = `
	SELECT id, org_id, kind, address, config, status, created_at
	FROM channel_connections`

func (s *Store) scanChannelConnection(row pgx.Row) (*ChannelConnection, error) {
	var c ChannelConnection
	err := row.Scan(&c.ID, &c.OrgID, &c.Kind, &c.Address, &c.Config, &c.Status, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetChannelConnectionByAddress resolves the connection an inbound message
// arrived on, keyed by (kind, address). This is where org identity enters the
// request path (design/002): the resolved connection carries OrgID downstream.
func (s *Store) GetChannelConnectionByAddress(ctx context.Context, kind, address string) (*ChannelConnection, error) {
	return s.scanChannelConnection(s.pool.QueryRow(ctx,
		channelConnectionSelect+` WHERE kind = $1 AND address = $2`, kind, address))
}

// GetChannelConnection resolves a conversation's connection so the outbound
// Sender can pick the adapter for its kind.
func (s *Store) GetChannelConnection(ctx context.Context, id string) (*ChannelConnection, error) {
	return s.scanChannelConnection(s.pool.QueryRow(ctx, channelConnectionSelect+` WHERE id = $1`, id))
}

// --- conversations ---

const conversationSelect = `
	SELECT id, org_id, customer_id, channel_id, COALESCE(run_id, ''), status,
	       attention_state, attention_reason, escalated_at, created_at, updated_at
	FROM conversations`

func (s *Store) scanConversation(row pgx.Row) (*Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.OrgID, &c.CustomerID, &c.ChannelID, &c.RunID, &c.Status,
		&c.AttentionState, &c.AttentionReason, &c.EscalatedAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) CreateConversation(ctx context.Context, orgID, customerID, channelID string) (*Conversation, error) {
	id := agentkit.NewID()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO conversations (id, org_id, customer_id, channel_id) VALUES ($1, $2, $3, $4)`,
		id, orgID, customerID, channelID)
	if err != nil {
		return nil, err
	}
	return s.GetConversation(ctx, id)
}

func (s *Store) GetConversation(ctx context.Context, id string) (*Conversation, error) {
	return s.scanConversation(s.pool.QueryRow(ctx, conversationSelect+` WHERE id = $1`, id))
}

// OpenConversationForCustomer returns the customer's open conversation, or
// ErrNotFound.
func (s *Store) OpenConversationForCustomer(ctx context.Context, orgID, customerID string) (*Conversation, error) {
	return s.scanConversation(s.pool.QueryRow(ctx,
		conversationSelect+` WHERE org_id = $1 AND customer_id = $2 AND status = 'open'
		ORDER BY created_at DESC LIMIT 1`, orgID, customerID))
}

func (s *Store) GetConversationByRunID(ctx context.Context, runID string) (*Conversation, error) {
	return s.scanConversation(s.pool.QueryRow(ctx, conversationSelect+` WHERE run_id = $1`, runID))
}

func (s *Store) SetConversationRun(ctx context.Context, conversationID, runID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE conversations SET run_id = $2, updated_at = now() WHERE id = $1`,
		conversationID, runID)
	return err
}

func (s *Store) CloseConversation(ctx context.Context, conversationID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE conversations SET status = 'closed', updated_at = now() WHERE id = $1`,
		conversationID)
	return err
}

// RaiseEscalation projects an escalate action onto the conversation: it flags
// the conversation for urgent human attention with the agent's reason. The
// escalate Action itself is the raised record on the append-only log; this is
// the current-view projection the dispatcher UI reads (design/001-escalation.md).
// Idempotent under the action pipeline's retries — the same escalation
// re-applied is a no-op change.
func (s *Store) RaiseEscalation(ctx context.Context, conversationID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE conversations
		SET attention_state = 'flagged', attention_reason = $2,
		    escalated_at = now(), updated_at = now()
		WHERE id = $1`,
		conversationID, reason)
	return err
}

// AcknowledgeEscalation marks a flagged conversation as engaged by a
// dispatcher. Only a flagged conversation can be acknowledged; the reason and
// escalated_at are kept so the projection still shows what the emergency was.
func (s *Store) AcknowledgeEscalation(ctx context.Context, conversationID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE conversations
		SET attention_state = 'acknowledged', updated_at = now()
		WHERE id = $1 AND attention_state = 'flagged'`,
		conversationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListConversations(ctx context.Context, orgID string) ([]Conversation, error) {
	// Flagged conversations sort to the top — the dispatcher sees emergencies
	// first — then most-recently-active within each tier.
	rows, err := s.pool.Query(ctx, conversationSelect+`
		WHERE org_id = $1
		ORDER BY (attention_state = 'flagged') DESC, updated_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := s.scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// --- messages ---

// AddMessage inserts a message (idempotent on ID) and bumps the
// conversation's updated_at.
func (s *Store) AddMessage(ctx context.Context, m Message) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		INSERT INTO messages (id, org_id, conversation_id, direction, author, body)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING`,
		m.ID, m.OrgID, m.ConversationID, m.Direction, m.Author, m.Body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE conversations SET updated_at = now() WHERE id = $1`, m.ConversationID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListMessages(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, org_id, conversation_id, direction, author, body, created_at
		FROM messages WHERE conversation_id = $1 ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.OrgID, &m.ConversationID, &m.Direction, &m.Author, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// --- jobs ---

const jobSelect = `
	SELECT id, org_id, conversation_id, customer_name, phone, address, issue, urgency, status, created_at, updated_at
	FROM jobs`

func (s *Store) scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.OrgID, &j.ConversationID, &j.CustomerName, &j.Phone,
		&j.Address, &j.Issue, &j.Urgency, &j.Status, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) GetJobByConversation(ctx context.Context, conversationID string) (*Job, error) {
	return s.scanJob(s.pool.QueryRow(ctx, jobSelect+` WHERE conversation_id = $1`, conversationID))
}

// UpsertJob creates the conversation's job if needed and applies the patch.
// A newly created job inherits phone and name from the conversation's
// customer, so channel-known contact details are never left blank waiting on
// the agent to re-collect them.
func (s *Store) UpsertJob(ctx context.Context, orgID, conversationID string, patch JobPatch) (*Job, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO jobs (id, org_id, conversation_id, phone, customer_name)
		SELECT $1, $2, c.id, cust.phone, cust.name
		FROM conversations c JOIN customers cust ON cust.id = c.customer_id
		WHERE c.id = $3
		ON CONFLICT (conversation_id) DO NOTHING`,
		agentkit.NewID(), orgID, conversationID)
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE jobs SET
			customer_name = COALESCE($2, customer_name),
			address       = COALESCE($3, address),
			issue         = COALESCE($4, issue),
			urgency       = COALESCE($5, urgency),
			updated_at    = now()
		WHERE conversation_id = $1`,
		conversationID, patch.CustomerName, patch.Address, patch.Issue, patch.Urgency)
	if err != nil {
		return nil, err
	}
	return s.GetJobByConversation(ctx, conversationID)
}

// CompleteIntake atomically marks the conversation's job intake_complete and
// closes the conversation, so the two never drift apart on a partial failure.
func (s *Store) CompleteIntake(ctx context.Context, conversationID string) (*Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE jobs SET status = 'intake_complete', updated_at = now()
		WHERE conversation_id = $1`, conversationID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("domain: no job for conversation %s", conversationID)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversations SET status = 'closed', updated_at = now() WHERE id = $1`,
		conversationID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetJobByConversation(ctx, conversationID)
}
