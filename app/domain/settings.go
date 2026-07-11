package domain

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"dispatch/agentkit"
)

const playbookSelect = `SELECT id,org_id,name,pack_id,agent,case_type,config,version,created_at FROM playbooks`

func (s *Store) GetOrganization(ctx context.Context, orgID, id string) (*Organization, error) {
	var o Organization
	err := s.pool.QueryRow(ctx, `SELECT id,name,settings,version,created_at FROM organizations WHERE id=$1 AND id=$2`, id, orgID).
		Scan(&o.ID, &o.Name, &o.Settings, &o.Version, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) ListPlaybooks(ctx context.Context, orgID string) ([]Playbook, error) {
	rows, err := s.pool.Query(ctx, playbookSelect+` WHERE org_id=$1 ORDER BY created_at,id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Playbook{}
	for rows.Next() {
		var p Playbook
		if err := scanPlaybook(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPlaybookForOrg(ctx context.Context, orgID, id string) (*Playbook, error) {
	var p Playbook
	err := scanPlaybook(s.pool.QueryRow(ctx, playbookSelect+` WHERE org_id=$1 AND id=$2`, orgID, id), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GetAgentBehavior returns the organization's singleton playbook. The database
// constraint on playbooks.org_id makes the lookup unambiguous.
func (s *Store) GetAgentBehavior(ctx context.Context, orgID string) (*Playbook, error) {
	var p Playbook
	err := scanPlaybook(s.pool.QueryRow(ctx, playbookSelect+` WHERE org_id=$1`, orgID), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) PlaybookForRun(ctx context.Context, orgID, runID string) (*Playbook, error) {
	p, _, err := s.PlaybookAndStageForRun(ctx, orgID, runID)
	return p, err
}

func (s *Store) PlaybookAndStageForRun(ctx context.Context, orgID, runID string) (*Playbook, string, error) {
	var p Playbook
	var stage string
	err := s.pool.QueryRow(ctx, `SELECT p.id,p.org_id,p.name,p.pack_id,p.agent,p.case_type,p.config,p.version,p.created_at,rb.stage
		FROM run_bindings rb JOIN playbooks p ON p.org_id=rb.org_id AND p.id=rb.playbook_id
		WHERE rb.org_id=$1 AND rb.run_id=$2`).
		Scan(&p.ID, &p.OrgID, &p.Name, &p.PackID, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt, &stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return &p, stage, nil
}

// AgentRuntimeSnapshotForRun captures every mutable input used to resolve an
// agent turn in one statement snapshot: behavior, organization knowledge,
// stage, conversation cursor, and entity versions.
func (s *Store) AgentRuntimeSnapshotForRun(ctx context.Context, orgID, runID string) (*AgentRuntimeSnapshot, error) {
	var snapshot AgentRuntimeSnapshot
	var customerID string
	var customerVersion int64
	var caseID *string
	var caseVersion *int64
	err := s.pool.QueryRow(ctx, `SELECT
		p.id,p.org_id,p.name,p.pack_id,p.agent,p.case_type,p.config,p.version,p.created_at,
		o.id,o.name,o.settings,o.version,o.created_at,
		rb.stage,c.id,c.context_revision,c.event_seq,c.customer_id,cu.version,rb.case_id,ca.version
		FROM run_bindings rb
		JOIN playbooks p ON p.org_id=rb.org_id AND p.id=rb.playbook_id
		JOIN organizations o ON o.id=rb.org_id
		JOIN conversations c ON c.org_id=rb.org_id AND c.id=rb.conversation_id
		JOIN customers cu ON cu.org_id=c.org_id AND cu.id=c.customer_id
		LEFT JOIN cases ca ON ca.org_id=rb.org_id AND ca.id=rb.case_id
		WHERE rb.org_id=$1 AND rb.run_id=$2`, orgID, runID).Scan(
		&snapshot.Playbook.ID, &snapshot.Playbook.OrgID, &snapshot.Playbook.Name,
		&snapshot.Playbook.PackID, &snapshot.Playbook.Agent, &snapshot.Playbook.CaseType,
		&snapshot.Playbook.Config, &snapshot.Playbook.Version, &snapshot.Playbook.CreatedAt,
		&snapshot.Organization.ID, &snapshot.Organization.Name, &snapshot.Organization.Settings,
		&snapshot.Organization.Version, &snapshot.Organization.CreatedAt,
		&snapshot.Stage, &snapshot.ConversationID, &snapshot.ContextRevision,
		&snapshot.EventToSeq, &customerID, &customerVersion, &caseID, &caseVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	dependencies := map[string]any{
		"conversation_id":      snapshot.ConversationID,
		"customer_id":          customerID,
		"customer_version":     customerVersion,
		"organization_id":      snapshot.Organization.ID,
		"organization_version": snapshot.Organization.Version,
		"playbook_id":          snapshot.Playbook.ID,
		"playbook_version":     snapshot.Playbook.Version,
		"stage":                snapshot.Stage,
	}
	if caseID != nil {
		dependencies["case_id"] = *caseID
		dependencies["case_version"] = *caseVersion
	}
	snapshot.DependencyVersions, err = json.Marshal(dependencies)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

var ErrIdempotencyKeyReuse = errors.New("domain: idempotency key reused with different input")

// UpdateAgentBehavior updates the organization singleton with command-level
// idempotency. A concurrent retry waits for and replays the first result,
// including a version conflict; the same key with different input or actor is
// rejected rather than being mistaken for a retry.
func (s *Store) UpdateAgentBehavior(ctx context.Context, orgID string, expected int64, behavior AgentBehavior, commandID, actor string) (*Playbook, error) {
	if commandID == "" {
		return nil, fmt.Errorf("domain: command_id is required")
	}
	if actor == "" {
		return nil, fmt.Errorf("domain: actor is required")
	}
	config, err := json.Marshal(struct {
		Schema int           `json:"schema"`
		Voice  AgentBehavior `json:"voice"`
	}{Schema: 2, Voice: behavior})
	if err != nil {
		return nil, err
	}
	request, err := json.Marshal(struct {
		ExpectedVersion int64         `json:"expected_version"`
		Behavior        AgentBehavior `json:"behavior"`
	}{ExpectedVersion: expected, Behavior: behavior})
	if err != nil {
		return nil, err
	}
	requestHash := fmt.Sprintf("%x", sha256.Sum256(request))

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `INSERT INTO settings_commands
		(org_id,command_id,entity_kind,entity_id,request_hash,actor,state)
		VALUES($1,$2,'agent_behavior',$1,$3,$4,'pending')
		ON CONFLICT (org_id,command_id) DO NOTHING`, orgID, commandID, requestHash, actor)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		p, outcome, err := replaySettingsCommand(ctx, tx, orgID, commandID, requestHash, actor)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		if outcome == "version_conflict" {
			return p, ErrVersionConflict
		}
		return p, nil
	}

	// A command ID from before durable reservations cannot be safely replayed
	// because no request fingerprint was stored.
	var legacyCommand bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM config_revisions WHERE org_id=$1 AND command_id=$2
	)`, orgID, commandID).Scan(&legacyCommand); err != nil {
		return nil, err
	}
	if legacyCommand {
		return nil, ErrIdempotencyKeyReuse
	}

	var p Playbook
	err = s.scanPlaybookTx(ctx, tx, orgID, true, &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if p.Version != expected {
		if err := completeSettingsCommand(ctx, tx, orgID, commandID, "version_conflict", httpStatusConflict, p); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &p, ErrVersionConflict
	}
	err = tx.QueryRow(ctx, `UPDATE playbooks SET config=$2,version=version+1 WHERE org_id=$1
		RETURNING id,org_id,name,pack_id,agent,case_type,config,version,created_at`, orgID, config).
		Scan(&p.ID, &p.OrgID, &p.Name, &p.PackID, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err = insertRevision(ctx, tx, orgID, "playbook", p.ID, p.Version, p.Config, commandID, actor); err != nil {
		return nil, err
	}
	if err = completeSettingsCommand(ctx, tx, orgID, commandID, "updated", httpStatusOK, p); err != nil {
		return nil, err
	}
	return &p, tx.Commit(ctx)
}

const (
	httpStatusOK       = 200
	httpStatusConflict = 409
)

type rowScanner interface {
	Scan(...any) error
}

func scanPlaybook(row rowScanner, p *Playbook) error {
	return row.Scan(&p.ID, &p.OrgID, &p.Name, &p.PackID, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt)
}

func (s *Store) scanPlaybookTx(ctx context.Context, tx pgx.Tx, orgID string, lock bool, p *Playbook) error {
	query := playbookSelect + ` WHERE org_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanPlaybook(tx.QueryRow(ctx, query, orgID), p)
}

func replaySettingsCommand(ctx context.Context, tx pgx.Tx, orgID, commandID, requestHash, actor string) (*Playbook, string, error) {
	var storedHash, storedActor, state, outcome string
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT request_hash,actor,state,COALESCE(outcome,''),result
		FROM settings_commands WHERE org_id=$1 AND command_id=$2 FOR UPDATE`, orgID, commandID).
		Scan(&storedHash, &storedActor, &state, &outcome, &raw)
	if err != nil {
		return nil, "", err
	}
	if storedHash != requestHash || storedActor != actor {
		return nil, "", ErrIdempotencyKeyReuse
	}
	if state != "completed" {
		return nil, "", fmt.Errorf("domain: settings command %q is incomplete", commandID)
	}
	var p Playbook
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "", fmt.Errorf("domain: decode settings command result: %w", err)
	}
	return &p, outcome, nil
}

func completeSettingsCommand(ctx context.Context, tx pgx.Tx, orgID, commandID, outcome string, status int, p Playbook) error {
	result, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE settings_commands
		SET state='completed',outcome=$3,result=$4,http_status=$5,completed_at=now()
		WHERE org_id=$1 AND command_id=$2 AND state='pending'`, orgID, commandID, outcome, result, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("domain: settings command %q was not pending", commandID)
	}
	return nil
}

func (s *Store) UpdateOrganizationSettings(ctx context.Context, orgID string, expected int64, settings json.RawMessage, commandID, actor string) (*Organization, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if replay, ok, err := revisionReplay(ctx, tx, orgID, commandID, "organization", orgID); err != nil {
		return nil, err
	} else if ok {
		var o Organization
		err = tx.QueryRow(ctx, `SELECT id,name,$2::jsonb,$3::bigint,created_at FROM organizations WHERE id=$1`, orgID, replay.Config, replay.Version).Scan(&o.ID, &o.Name, &o.Settings, &o.Version, &o.CreatedAt)
		if err != nil {
			return nil, err
		}
		return &o, tx.Commit(ctx)
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT version FROM organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, ErrVersionConflict
	}
	var o Organization
	err = tx.QueryRow(ctx, `UPDATE organizations SET settings=$2,version=version+1 WHERE id=$1 RETURNING id,name,settings,version,created_at`, orgID, settings).Scan(&o.ID, &o.Name, &o.Settings, &o.Version, &o.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err = insertRevision(ctx, tx, orgID, "organization", orgID, o.Version, o.Settings, commandID, actor); err != nil {
		return nil, err
	}
	return &o, tx.Commit(ctx)
}

func (s *Store) ListChannelConnections(ctx context.Context, orgID string) ([]ChannelConnection, error) {
	rows, err := s.pool.Query(ctx, channelConnectionSelect+` WHERE org_id=$1 ORDER BY created_at,id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChannelConnection{}
	for rows.Next() {
		c, err := s.scanChannelConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) CreateChannelConnection(ctx context.Context, c ChannelConnection, commandID, actor string) (*ChannelConnection, error) {
	if commandID == "" {
		return nil, fmt.Errorf("domain: command_id is required")
	}
	if actor == "" {
		return nil, fmt.Errorf("domain: actor is required")
	}
	if len(c.Config) == 0 {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Status == "" {
		c.Status = "active"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Serialize one command key so concurrent retries cannot create two rows
	// before the config_revisions uniqueness check chooses a winner.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, c.OrgID+"\x1f"+commandID); err != nil {
		return nil, err
	}
	if replay, ok, err := channelRevisionReplay(ctx, tx, c.OrgID, commandID); err != nil {
		return nil, err
	} else if ok {
		if replay.EntityKind != "channel_connection" {
			return nil, ErrIdempotencyKeyReuse
		}
		var snapshot struct {
			Kind    string `json:"kind"`
			Address string `json:"address"`
		}
		if replay.Actor != actor || json.Unmarshal(replay.Config, &snapshot) != nil ||
			snapshot.Kind != c.Kind || snapshot.Address != c.Address {
			return nil, ErrIdempotencyKeyReuse
		}
		out, err := scanChannelRevision(ctx, tx, c.OrgID, replay.EntityID, replay)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit(ctx)
	}
	var reservedBySettings bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settings_commands WHERE org_id=$1 AND command_id=$2)`, c.OrgID, commandID).
		Scan(&reservedBySettings); err != nil {
		return nil, err
	}
	if reservedBySettings {
		return nil, ErrIdempotencyKeyReuse
	}
	if c.ID == "" {
		c.ID = agentkit.NewID()
	}
	// Routing is server-owned while an organization has exactly one playbook.
	// Ignore any caller-supplied value rather than exposing a hidden selector.
	if err = tx.QueryRow(ctx, `SELECT id FROM playbooks WHERE org_id=$1`, c.OrgID).Scan(&c.DefaultPlaybookID); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO channel_connections(id,org_id,kind,address,config,status,default_playbook_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING version,created_at`, c.ID, c.OrgID, c.Kind, c.Address, c.Config, c.Status, c.DefaultPlaybookID).Scan(&c.Version, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]any{"kind": c.Kind, "address": c.Address, "config": json.RawMessage(c.Config), "status": c.Status, "default_playbook_id": c.DefaultPlaybookID})
	if err = insertRevision(ctx, tx, c.OrgID, "channel_connection", c.ID, c.Version, raw, commandID, actor); err != nil {
		return nil, err
	}
	return &c, tx.Commit(ctx)
}

type revision struct {
	EntityKind string
	EntityID   string
	Version    int64
	Config     json.RawMessage
	Actor      string
}

func channelRevisionReplay(ctx context.Context, tx pgx.Tx, orgID, commandID string) (revision, bool, error) {
	var r revision
	err := tx.QueryRow(ctx, `SELECT entity_kind,entity_id,version,config,actor FROM config_revisions
		WHERE org_id=$1 AND command_id=$2`, orgID, commandID).
		Scan(&r.EntityKind, &r.EntityID, &r.Version, &r.Config, &r.Actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func revisionReplay(ctx context.Context, tx pgx.Tx, orgID, commandID, kind, id string) (revision, bool, error) {
	var r revision
	err := tx.QueryRow(ctx, `SELECT version,config FROM config_revisions WHERE org_id=$1 AND command_id=$2 AND entity_kind=$3 AND entity_id=$4`, orgID, commandID, kind, id).Scan(&r.Version, &r.Config)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}
func insertRevision(ctx context.Context, tx pgx.Tx, orgID, kind, id string, version int64, config json.RawMessage, commandID, actor string) error {
	if commandID == "" {
		return fmt.Errorf("domain: command_id is required")
	}
	_, err := tx.Exec(ctx, `INSERT INTO config_revisions(id,org_id,entity_kind,entity_id,version,config,command_id,actor) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, agentkit.NewID(), orgID, kind, id, version, config, commandID, actor)
	return err
}
func scanChannelRevision(ctx context.Context, tx pgx.Tx, orgID, id string, r revision) (*ChannelConnection, error) {
	var c ChannelConnection
	err := tx.QueryRow(ctx, channelConnectionSelect+` WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&c.ID, &c.OrgID, &c.Kind, &c.Address, &c.Config, &c.Status, &c.DefaultPlaybookID, &c.Version, &c.CreatedAt)
	if err == nil {
		var snapshot struct {
			Kind              string          `json:"kind"`
			Address           string          `json:"address"`
			Config            json.RawMessage `json:"config"`
			Status            string          `json:"status"`
			DefaultPlaybookID string          `json:"default_playbook_id"`
		}
		if json.Unmarshal(r.Config, &snapshot) == nil {
			if snapshot.Kind != "" {
				c.Kind = snapshot.Kind
			}
			if snapshot.Address != "" {
				c.Address = snapshot.Address
			}
			if len(snapshot.Config) > 0 {
				c.Config = snapshot.Config
			}
			if snapshot.Status != "" {
				c.Status = snapshot.Status
			}
			if snapshot.DefaultPlaybookID != "" {
				c.DefaultPlaybookID = snapshot.DefaultPlaybookID
			}
		}
		c.Version = r.Version
	}
	return &c, err
}
