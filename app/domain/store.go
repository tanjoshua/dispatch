package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
// name is never overwritten. Safe under concurrent first messages: an insert
// that loses the identity's (org, kind, address) unique race falls back to the
// winner's row.
func (s *Store) GetOrCreateCustomerByIdentity(ctx context.Context, orgID, kind, address, name string) (*Customer, error) {
	for attempt := 0; attempt < 3; attempt++ {
		var customerID string
		err := s.pool.QueryRow(ctx, `
			SELECT customer_id FROM contact_identities
			WHERE org_id = $1 AND channel_kind = $2 AND address = $3`,
			orgID, kind, address).Scan(&customerID)
		switch {
		case err == nil:
			if name != "" {
				if _, err := s.pool.Exec(ctx, `
					UPDATE customers SET name = $2 WHERE id = $1 AND name = ''`,
					customerID, name); err != nil {
					return nil, err
				}
			}
			return s.GetCustomer(ctx, customerID)
		case !errors.Is(err, pgx.ErrNoRows):
			return nil, err
		}
		created, err := s.tryCreateCustomerWithIdentity(ctx, orgID, kind, address, name)
		if err != nil {
			return nil, err
		}
		if created != "" {
			return s.GetCustomer(ctx, created)
		}
		// Lost the create race; loop to read the winner's identity.
	}
	return nil, fmt.Errorf("domain: could not resolve customer for (%s, %s)", kind, address)
}

// tryCreateCustomerWithIdentity inserts a customer and its identity in one
// transaction. Returns "" (rolled back, no error) when a concurrent creator won
// the identity's unique constraint first.
func (s *Store) tryCreateCustomerWithIdentity(ctx context.Context, orgID, kind, address, name string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	customerID := agentkit.NewID()
	if _, err := tx.Exec(ctx, `
		INSERT INTO customers (id, org_id, name) VALUES ($1, $2, $3)`,
		customerID, orgID, name); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO contact_identities (id, org_id, customer_id, channel_kind, address)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (org_id, channel_kind, address) DO NOTHING`,
		agentkit.NewID(), orgID, customerID, kind, address)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", nil // lost the race; the deferred rollback discards our customer
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return customerID, nil
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
	SELECT id, org_id, kind, address, config, status, COALESCE(default_playbook_id, ''), created_at
	FROM channel_connections`

func (s *Store) scanChannelConnection(row pgx.Row) (*ChannelConnection, error) {
	var c ChannelConnection
	err := row.Scan(&c.ID, &c.OrgID, &c.Kind, &c.Address, &c.Config, &c.Status, &c.DefaultPlaybookID, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// --- playbooks ---

// GetPlaybook resolves the playbook a run should use, selected off the channel
// connection an inbound message arrived on (design/004-domain-remodel.md §8).
func (s *Store) GetPlaybook(ctx context.Context, id string) (*Playbook, error) {
	var p Playbook
	err := s.pool.QueryRow(ctx, `
		SELECT id, org_id, name, agent, case_type, config, created_at
		FROM playbooks WHERE id = $1`, id).
		Scan(&p.ID, &p.OrgID, &p.Name, &p.Agent, &p.CaseType, &p.Config, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
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
	       attention_state, attention_reason, escalated_at, thread_summary,
	       created_at, updated_at
	FROM conversations`

func (s *Store) scanConversation(row pgx.Row) (*Conversation, error) {
	var c Conversation
	err := row.Scan(&c.ID, &c.OrgID, &c.CustomerID, &c.ChannelID, &c.Status,
		&c.AttentionState, &c.AttentionReason, &c.EscalatedAt, &c.ThreadSummary,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) CreateConversation(ctx context.Context, orgID, customerID, channelID string) (*Conversation, error) {
	// One thread per (customer, channel) is a unique constraint; a concurrent
	// creator winning it means the thread already exists — return it.
	id := agentkit.NewID()
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO conversations (id, org_id, customer_id, channel_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, customer_id, channel_id) DO NOTHING`,
		id, orgID, customerID, channelID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return s.ThreadForCustomerChannel(ctx, orgID, customerID, channelID)
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

// ClaimRunBinding makes candidateRunID the thread's live run — binding it as a
// task on the conversation, under a playbook — unless another bound run is
// still running, and returns the run that won. It locks the conversation row so
// two concurrent inbound deliveries can't each create a live run on one thread
// (a thread has at most one run awaiting customer input, design/004 §6); the
// loser's caller retires its candidate. The bound case is set later, when the
// run's first update_case creates it. playbookID may be empty when a connection
// has no playbook (the case type then falls back to the default).
//
// Reading runs.status here reaches into agentkit's table from app SQL — the
// schemas already share one database (run_bindings carries the FK), and a
// check-then-insert across the two stores is exactly the race this closes.
func (s *Store) ClaimRunBinding(ctx context.Context, orgID, conversationID, candidateRunID, taskKind, playbookID string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var locked string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM conversations WHERE id = $1 FOR UPDATE`, conversationID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	var live string
	err = tx.QueryRow(ctx, `
		SELECT rb.run_id FROM run_bindings rb
		JOIN runs r ON r.id = rb.run_id
		WHERE rb.conversation_id = $1 AND r.status = 'running'
		ORDER BY rb.created_at DESC LIMIT 1`, conversationID).Scan(&live)
	switch {
	case err == nil:
		return live, tx.Commit(ctx)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}

	var pb *string
	if playbookID != "" {
		pb = &playbookID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO run_bindings (run_id, org_id, conversation_id, task_kind, playbook_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (run_id) DO NOTHING`,
		candidateRunID, orgID, conversationID, taskKind, pb); err != nil {
		return "", err
	}
	return candidateRunID, tx.Commit(ctx)
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
//
// The returned bool is true only when this call transitioned the conversation
// *into* flagged. Notification (OVERVIEW §6.3 #13) keys on that transition:
// activity retries and repeat escalates on an already-flagged thread update
// the reason but never re-page the dispatcher.
func (s *Store) RaiseEscalation(ctx context.Context, conversationID, reason string) (bool, error) {
	var prev AttentionState
	row := s.pool.QueryRow(ctx, `
		UPDATE conversations c
		SET attention_state = 'flagged', attention_reason = $2,
		    escalated_at = now(), updated_at = now()
		FROM (SELECT id, attention_state FROM conversations WHERE id = $1 FOR UPDATE) prev
		WHERE c.id = prev.id
		RETURNING prev.attention_state`,
		conversationID, reason)
	if err := row.Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	return prev != AttentionFlagged, nil
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

const messageSelect = `
	SELECT id, org_id, conversation_id, direction, author, body,
	       COALESCE(provider_message_id, ''), created_at
	FROM messages`

func scanMessage(row pgx.Row) (*Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.OrgID, &m.ConversationID, &m.Direction, &m.Author,
		&m.Body, &m.ProviderMessageID, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// AddMessage inserts an outbound message (idempotent on ID — the retried-send
// dedupe) and bumps the conversation's updated_at. Inbound messages go through
// AddInboundMessage, which dedupes on the provider's message ID instead.
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

// AddInboundMessage inserts an inbound message, deduplicating on the provider's
// message ID per conversation: a webhook retry or provider duplicate returns
// the already-stored row instead of inserting a second one (OVERVIEW §6.1 #2).
// Returns the canonical message and whether this call inserted it.
func (s *Store) AddInboundMessage(ctx context.Context, m Message) (*Message, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		INSERT INTO messages (id, org_id, conversation_id, direction, author, body, provider_message_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING`,
		m.ID, m.OrgID, m.ConversationID, m.Direction, m.Author, m.Body, nullIfEmpty(m.ProviderMessageID))
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 0 {
		// Duplicate delivery: hand back the original so the caller signals the
		// run with the canonical message ID.
		if m.ProviderMessageID == "" {
			msg, err := scanMessage(s.pool.QueryRow(ctx, messageSelect+` WHERE id = $1`, m.ID))
			return msg, false, err
		}
		msg, err := scanMessage(s.pool.QueryRow(ctx,
			messageSelect+` WHERE conversation_id = $1 AND provider_message_id = $2`,
			m.ConversationID, m.ProviderMessageID))
		return msg, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE conversations SET updated_at = now() WHERE id = $1`, m.ConversationID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	msg, err := scanMessage(s.pool.QueryRow(ctx, messageSelect+` WHERE id = $1`, m.ID))
	return msg, true, err
}

// ListRecentMessages returns the thread's newest limit messages, oldest first
// — the briefing's recent-message window.
func (s *Store) ListRecentMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	rows, err := s.pool.Query(ctx,
		messageSelect+` WHERE conversation_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`,
		conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *Store) ListMessages(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.pool.Query(ctx,
		messageSelect+` WHERE conversation_id = $1 ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
	// retries can't create two cases for one run. The case type is the run's
	// playbook's case_type (design/004 §8), defaulting when no playbook is bound.
	var caseID *string
	var caseType string
	if err := tx.QueryRow(ctx, `
		SELECT rb.case_id, COALESCE(pb.case_type, 'field_service_job')
		FROM run_bindings rb
		LEFT JOIN playbooks pb ON pb.id = rb.playbook_id
		WHERE rb.run_id = $1 FOR UPDATE OF rb`, runID).Scan(&caseID, &caseType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("domain: no run binding for run %s", runID)
		}
		return nil, err
	}
	if caseID == nil {
		id := agentkit.NewID()
		if _, err := tx.Exec(ctx, `
			INSERT INTO cases (id, org_id, conversation_id, customer_id, type)
			SELECT $1, $2, c.id, c.customer_id, $4
			FROM conversations c WHERE c.id = $3`,
			id, orgID, conversationID, caseType); err != nil {
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

// BindRunToLatestCase attaches an unbound run to the thread's most recent case
// — the continue_case tool: the run's work targets that case instead of
// opening a new one, and a completed case reopens for intake. Idempotent under
// activity retries: a run already bound returns its bound case unchanged.
func (s *Store) BindRunToLatestCase(ctx context.Context, runID string) (*Case, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var conversationID string
	var boundCaseID *string
	if err := tx.QueryRow(ctx, `
		SELECT conversation_id, case_id FROM run_bindings
		WHERE run_id = $1 FOR UPDATE`, runID).Scan(&conversationID, &boundCaseID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("domain: no run binding for run %s", runID)
		}
		return nil, err
	}
	if boundCaseID != nil {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return s.GetCase(ctx, *boundCaseID)
	}

	var caseID string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM cases WHERE conversation_id = $1
		ORDER BY created_at DESC LIMIT 1`, conversationID).Scan(&caseID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("domain: no previous case on this thread to continue")
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE run_bindings SET case_id = $2 WHERE run_id = $1`, runID, caseID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE cases SET status = 'intake', updated_at = now()
		WHERE id = $1 AND status = 'intake_complete'`, caseID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.GetCase(ctx, caseID)
}

// threadSummaryLines caps the rolling summary: enough for a briefing to cover
// the thread's recent history without growing unboundedly.
const threadSummaryLines = 5

// AppendThreadSummary appends one dated line — the dispatcher-approved
// close_case summary — to the thread's rolling summary, keeping the newest
// threadSummaryLines. Idempotent under tool-execution retries: a line already
// present is not re-appended.
func (s *Store) AppendThreadSummary(ctx context.Context, conversationID, line string) error {
	if line == "" {
		return nil
	}
	var current string
	if err := s.pool.QueryRow(ctx, `
		SELECT thread_summary FROM conversations WHERE id = $1`, conversationID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if strings.Contains(current, line) {
		return nil
	}
	lines := []string{}
	if current != "" {
		lines = strings.Split(current, "\n")
	}
	lines = append(lines, line)
	if len(lines) > threadSummaryLines {
		lines = lines[len(lines)-threadSummaryLines:]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE conversations SET thread_summary = $2, updated_at = now() WHERE id = $1`,
		conversationID, strings.Join(lines, "\n"))
	return err
}

// CompleteCaseForRun marks the run's bound case intake_complete. A run that
// never touched a case (a triage run that only answered a question) completes
// with a nil case — ending the task without one is legitimate. It does NOT
// close the thread — threads are persistent (design/004-domain-remodel.md §4);
// the next customer message starts a fresh run on the same thread.
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
		return nil, nil
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE cases SET status = 'intake_complete', updated_at = now() WHERE id = $1`, *caseID); err != nil {
		return nil, err
	}
	return s.GetCase(ctx, *caseID)
}
