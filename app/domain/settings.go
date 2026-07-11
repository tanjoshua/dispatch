package domain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"dispatch/agentkit"
)

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
	rows, err := s.pool.Query(ctx, `SELECT id,org_id,name,agent,case_type,config,version,created_at FROM playbooks WHERE org_id=$1 ORDER BY created_at,id`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Playbook{}
	for rows.Next() {
		var p Playbook
		if err := rows.Scan(&p.ID, &p.OrgID, &p.Name, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPlaybookForOrg(ctx context.Context, orgID, id string) (*Playbook, error) {
	var p Playbook
	err := s.pool.QueryRow(ctx, `SELECT id,org_id,name,agent,case_type,config,version,created_at FROM playbooks WHERE org_id=$1 AND id=$2`, orgID, id).
		Scan(&p.ID, &p.OrgID, &p.Name, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt)
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
	err := s.pool.QueryRow(ctx, `SELECT p.id,p.org_id,p.name,p.agent,p.case_type,p.config,p.version,p.created_at,rb.stage FROM run_bindings rb JOIN playbooks p ON p.id=rb.playbook_id WHERE rb.org_id=$1 AND rb.run_id=$2`, orgID, runID).
		Scan(&p.ID, &p.OrgID, &p.Name, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt, &stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return &p, stage, nil
}

func (s *Store) UpdatePlaybookConfig(ctx context.Context, orgID, id string, expected int64, config json.RawMessage, commandID, actor string) (*Playbook, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if replay, ok, err := revisionReplay(ctx, tx, orgID, commandID, "playbook", id); err != nil {
		return nil, err
	} else if ok {
		p, err := scanPlaybookRevision(ctx, tx, orgID, id, replay)
		if err != nil {
			return nil, err
		}
		return p, tx.Commit(ctx)
	}
	var current int64
	if err := tx.QueryRow(ctx, `SELECT version FROM playbooks WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, id).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, ErrVersionConflict
	}
	var p Playbook
	err = tx.QueryRow(ctx, `UPDATE playbooks SET config=$3,version=version+1 WHERE org_id=$1 AND id=$2 RETURNING id,org_id,name,agent,case_type,config,version,created_at`, orgID, id, config).Scan(&p.ID, &p.OrgID, &p.Name, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err = insertRevision(ctx, tx, orgID, "playbook", id, p.Version, p.Config, commandID, actor); err != nil {
		return nil, err
	}
	return &p, tx.Commit(ctx)
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

func (s *Store) UpdateChannelConnectionPlaybook(ctx context.Context, orgID, id, playbookID string, expected int64, commandID, actor string) (*ChannelConnection, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if replay, ok, err := revisionReplay(ctx, tx, orgID, commandID, "channel_connection", id); err != nil {
		return nil, err
	} else if ok {
		c, err := scanChannelRevision(ctx, tx, orgID, id, replay)
		if err != nil {
			return nil, err
		}
		return c, tx.Commit(ctx)
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM playbooks WHERE org_id=$1 AND id=$2)`, orgID, playbookID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT version FROM channel_connections WHERE org_id=$1 AND id=$2 FOR UPDATE`, orgID, id).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if current != expected {
		return nil, ErrVersionConflict
	}
	var c ChannelConnection
	err = tx.QueryRow(ctx, `UPDATE channel_connections SET default_playbook_id=$3,version=version+1 WHERE org_id=$1 AND id=$2 RETURNING id,org_id,kind,address,config,status,COALESCE(default_playbook_id,''),version,created_at`, orgID, id, playbookID).Scan(&c.ID, &c.OrgID, &c.Kind, &c.Address, &c.Config, &c.Status, &c.DefaultPlaybookID, &c.Version, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]any{"default_playbook_id": c.DefaultPlaybookID, "config": json.RawMessage(c.Config)})
	if err = insertRevision(ctx, tx, orgID, "channel_connection", id, c.Version, raw, commandID, actor); err != nil {
		return nil, err
	}
	return &c, tx.Commit(ctx)
}

func (s *Store) CreateChannelConnection(ctx context.Context, c ChannelConnection, commandID, actor string) (*ChannelConnection, error) {
	if c.ID == "" {
		c.ID = agentkit.NewID()
	}
	if commandID == "" {
		commandID = c.ID
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
	if replay, ok, err := revisionReplay(ctx, tx, c.OrgID, commandID, "channel_connection", c.ID); err != nil {
		return nil, err
	} else if ok {
		out, err := scanChannelRevision(ctx, tx, c.OrgID, c.ID, replay)
		if err != nil {
			return nil, err
		}
		return out, tx.Commit(ctx)
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM playbooks WHERE org_id=$1 AND id=$2)`, c.OrgID, c.DefaultPlaybookID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
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
	Version int64
	Config  json.RawMessage
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
func scanPlaybookRevision(ctx context.Context, tx pgx.Tx, orgID, id string, r revision) (*Playbook, error) {
	var p Playbook
	err := tx.QueryRow(ctx, `SELECT id,org_id,name,agent,case_type,$3::jsonb,$4::bigint,created_at FROM playbooks WHERE org_id=$1 AND id=$2`, orgID, id, r.Config, r.Version).Scan(&p.ID, &p.OrgID, &p.Name, &p.Agent, &p.CaseType, &p.Config, &p.Version, &p.CreatedAt)
	return &p, err
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
