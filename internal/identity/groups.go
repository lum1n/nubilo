package identity

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"nubilo/internal/ids"
)

var (
	ErrGroupName   = errors.New("identity: invalid group name")
	ErrGroupExists = errors.New("identity: group name taken")
)

// Group is a named set of users. Sharing a collection with a group gives
// every member that access; admins manage groups.
type Group struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	CreatedAt int64    `json:"created_at"`
	Members   []string `json:"members"` // user IDs
}

func groupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || strings.ContainsRune(name, 0) {
		return "", ErrGroupName
	}
	return name, nil
}

// CreateGroup creates a group with the given members (user IDs).
func (s *Service) CreateGroup(ctx context.Context, name string, members []string) (*Group, error) {
	name, err := groupName(name)
	if err != nil {
		return nil, err
	}
	g := &Group{ID: ids.New(), Name: name, CreatedAt: s.now().UnixMilli()}
	err = s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM groups WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrGroupExists
		}
		if _, err := tx.Exec(`INSERT INTO groups(id, name, created_at) VALUES (?, ?, ?)`, g.ID, g.Name, g.CreatedAt); err != nil {
			return err
		}
		return setMembersTx(tx, g.ID, members)
	})
	if err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, g.ID)
}

// UpdateGroup renames a group (name != "") and/or replaces its members
// (members != nil).
func (s *Service) UpdateGroup(ctx context.Context, id, name string, members []string) (*Group, error) {
	err := s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM groups WHERE id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if name != "" {
			clean, err := groupName(name)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT COUNT(*) FROM groups WHERE name = ? AND id <> ?`, clean, id).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return ErrGroupExists
			}
			if _, err := tx.Exec(`UPDATE groups SET name = ? WHERE id = ?`, clean, id); err != nil {
				return err
			}
		}
		if members != nil {
			return setMembersTx(tx, id, members)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, id)
}

func setMembersTx(tx *sql.Tx, groupID string, members []string) error {
	if _, err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, uid := range members {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, uid).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO group_members(group_id, user_id) VALUES (?, ?)`, groupID, uid); err != nil {
			return err
		}
	}
	return nil
}

// DeleteGroup removes a group and every share made to it.
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	return s.Store.WithWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM shares WHERE principal_type = 'group' AND principal_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM groups WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Service) GetGroup(ctx context.Context, id string) (*Group, error) {
	var g Group
	err := s.Store.DB.QueryRowContext(ctx, `SELECT id, name, created_at FROM groups WHERE id = ?`, id).Scan(&g.ID, &g.Name, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	all, err := s.groupMembers(ctx)
	if err != nil {
		return nil, err
	}
	g.Members = nonNil(all[g.ID])
	return &g, nil
}

func (s *Service) GetGroupByName(ctx context.Context, name string) (*Group, error) {
	var id string
	err := s.Store.DB.QueryRowContext(ctx, `SELECT id FROM groups WHERE name = ?`, strings.TrimSpace(name)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.GetGroup(ctx, id)
}

func (s *Service) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `SELECT id, name, created_at FROM groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Group{}
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	all, err := s.groupMembers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Members = nonNil(all[out[i].ID])
	}
	return out, nil
}

func (s *Service) groupMembers(ctx context.Context) (map[string][]string, error) {
	rows, err := s.Store.DB.QueryContext(ctx, `
		SELECT gm.group_id, gm.user_id FROM group_members gm JOIN users u ON u.id = gm.user_id ORDER BY u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var g, u string
		if err := rows.Scan(&g, &u); err != nil {
			return nil, err
		}
		out[g] = append(out[g], u)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
