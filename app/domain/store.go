package domain

import (
	"context"
	"encoding/json"
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

// GetOrCreateCustomerByIdentity resolves the customer reachable at
// (kind, address), creating the customer and its identity together the first
// time. This is where an inbound message's channel address maps to a CRM
// customer (design/004-domain-remodel.md §3) — replacing the old phone-keyed
// customer. A blank existing name is filled if we now have one; an existing
// name is never overwritten.
func (s *Store) GetOrCreateCustomerByIdentity(ctx context.Context, orgID, kind, address, name string) (*Customer, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var customerID string
	err = tx.QueryRow(ctx, `
		SELECT customer_id FROM contact_identities
		WHERE org_id = $1 AND channel_kind = $2 AND address = $3`,
		orgID, kind, address).Scan(&customerID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		customerID = agentkit.NewID()
		if _, err := tx.Exec(ctx, `
			INSERT INTO customers (id, org_id, name) VALUES ($1, $2, $3)`,
			customerID, orgID, name); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO contact_identities (id, org_id, customer_id, channel_kind, address)
			VALUES ($1, $2, $3, $4, $5)`,
			agentkit.NewID(), orgID, customerID, kind, address); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if name != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE customers SET name = $2 WHERE id = $1 AND name = ''`,
				customerID, name); err != nil {
				return nil, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetCustomer(ctx, customerID)
}

func (s *Store) GetCustomer(ctx context.Context, id string) (*Customer, error) {
	var c Customer
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, name, created_at FROM customers WHERE id = $1`, id).
		Scan(&c.ID, &c.OrgID, &c.Name, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// SetCustomerName sets the customer's name. The intake agent collects the name
// as part of the conversation; it is an attribute of the Customer (the CRM
// aggregate), not of the case (design/004 §5), so update_case routes it here.
func (s *Store) SetCustomerName(ctx context.Context, customerID, name string) error {
	if name == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE customers SET name = $2 WHERE id = $1`, customerID, name)
	return err
}

// ContactAddressForConversation returns the customer-side address for a
// conversation — the customer's identity on that conversation's channel kind.
// The outbound Sender uses it to address delivery, and the API surfaces it as
// the thread's contact (design/004-domain-remodel.md §3). Empty if no matching
// identity exists.
func (s *Store) ContactAddressForConversation(ctx context.Context, conversationID string) (string, error) {
	var addr string
	err := s.pool.QueryRow(ctx, `
		SELECT ci.address
		FROM conversations conv
		JOIN channel_connections cc ON cc.id = conv.channel_id
		JOIN contact_identities ci
		  ON ci.customer_id = conv.customer_id AND ci.channel_kind = cc.kind
		WHERE conv.id = $1`, conversationID).Scan(&addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return addr, nil
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
	SELECT id, org_id, customer_id, channel_id, status,
	       attention_state, attention_reason, escalated_at, created_at, updated_at
	FROM conversations`

func (s *Store) scanConversation(row pgx.Row) (*Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.OrgID, &c.CustomerID, &c.ChannelID, &c.Status,
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

// ThreadForCustomerChannel returns the customer's persistent thread on a
// channel connection, or ErrNotFound. There is one thread per (customer,
// channel); it is durable across runs and cases, so this is not gated on status
// (design/004-domain-remodel.md §4).
func (s *Store) ThreadForCustomerChannel(ctx context.Context, orgID, customerID, channelID string) (*Conversation, error) {
	return s.scanConversation(s.pool.QueryRow(ctx,
		conversationSelect+` WHERE org_id = $1 AND customer_id = $2 AND channel_id = $3
		ORDER BY created_at DESC LIMIT 1`, orgID, customerID, channelID))
}

// GetConversationByRunID resolves the conversation a run is bound to, via
// run_bindings (a run no longer lives on the conversation row). Used by the
// intake tools to find the thread from the run context.
func (s *Store) GetConversationByRunID(ctx context.Context, runID string) (*Conversation, error) {
	return s.scanConversation(s.pool.QueryRow(ctx,
		conversationSelect+` WHERE id = (SELECT conversation_id FROM run_bindings WHERE run_id = $1)`, runID))
}

// --- run bindings ---

// CreateRunBinding records that a run is a task on a conversation. The bound
// case is set later, when the run's first update_case creates it
// (design/004-domain-remodel.md §6).
func (s *Store) CreateRunBinding(ctx context.Context, runID, orgID, conversationID, taskKind string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO run_bindings (run_id, org_id, conversation_id, task_kind)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (run_id) DO NOTHING`,
		runID, orgID, conversationID, taskKind)
	return err
}

// LatestRunIDForConversation returns the most recent run bound to a thread, or
// "" if none. Callers check the run's status (via the agentkit store) to decide
// whether it is live — a thread has many runs over its life, at most one active.
func (s *Store) LatestRunIDForConversation(ctx context.Context, conversationID string) (string, error) {
	var runID string
	err := s.pool.QueryRow(ctx, `
		SELECT run_id FROM run_bindings
		WHERE conversation_id = $1 ORDER BY created_at DESC LIMIT 1`, conversationID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return runID, nil
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

// --- cases ---

const caseSelect = `
	SELECT id, org_id, customer_id, conversation_id, type, status, data, created_at, updated_at
	FROM cases`

func (s *Store) scanCase(row pgx.Row) (*Case, error) {
	var c Case
	err := row.Scan(&c.ID, &c.OrgID, &c.CustomerID, &c.ConversationID,
		&c.Type, &c.Status, &c.Data, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) GetCase(ctx context.Context, id string) (*Case, error) {
	return s.scanCase(s.pool.QueryRow(ctx, caseSelect+` WHERE id = $1`, id))
}

// CurrentCaseForConversation returns the thread's most recent case, or
// ErrNotFound. A persistent thread can carry many cases; the dispatcher UI shows
// the latest (design/004-domain-remodel.md §5).
func (s *Store) CurrentCaseForConversation(ctx context.Context, conversationID string) (*Case, error) {
	return s.scanCase(s.pool.QueryRow(ctx,
		caseSelect+` WHERE conversation_id = $1 ORDER BY created_at DESC LIMIT 1`, conversationID))
}

// UpsertCaseForRun creates the case the run is working (binding it to the run on
// first call) if needed, then shallow-merges the patch into its Data bag
// (top-level keys). The case belongs to the run, not the thread, so each intake
// run produces its own case — the basis for many cases per thread
// (design/004-domain-remodel.md §6). The patch is the playbook's case-schema
// fields as raw JSON; the store stays schema-agnostic, so a playbook-driven
// schema needs no store change (§5, §8).
func (s *Store) UpsertCaseForRun(ctx context.Context, runID, orgID, conversationID string, patch json.RawMessage) (*Case, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Find the run's bound case, locking the binding so concurrent activity
	// retries can't create two cases for one run.
	var caseID *string
	if err := tx.QueryRow(ctx, `
		SELECT case_id FROM run_bindings WHERE run_id = $1 FOR UPDATE`, runID).Scan(&caseID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("domain: no run binding for run %s", runID)
		}
		return nil, err
	}
	if caseID == nil {
		id := agentkit.NewID()
		if _, err := tx.Exec(ctx, `
			INSERT INTO cases (id, org_id, conversation_id, customer_id, type)
			SELECT $1, $2, c.id, c.customer_id, 'field_service_job'
			FROM conversations c WHERE c.id = $3`,
			id, orgID, conversationID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE run_bindings SET case_id = $2 WHERE run_id = $1`, runID, id); err != nil {
			return nil, err
		}
		caseID = &id
	}
	if len(patch) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE cases SET data = data || $2::jsonb, updated_at = now() WHERE id = $1`,
			*caseID, patch); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetCase(ctx, *caseID)
}

// CompleteCaseForRun marks the run's bound case intake_complete. It does NOT
// close the thread — threads are persistent (design/004-domain-remodel.md §4);
// the next customer message starts a fresh run and a fresh case on the same
// thread.
func (s *Store) CompleteCaseForRun(ctx context.Context, runID string) (*Case, error) {
	var caseID *string
	if err := s.pool.QueryRow(ctx, `
		SELECT case_id FROM run_bindings WHERE run_id = $1`, runID).Scan(&caseID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("domain: no run binding for run %s", runID)
		}
		return nil, err
	}
	if caseID == nil {
		return nil, fmt.Errorf("domain: run %s has no case to complete", runID)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE cases SET status = 'intake_complete', updated_at = now() WHERE id = $1`, *caseID); err != nil {
		return nil, err
	}
	return s.GetCase(ctx, *caseID)
}
