package repository

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

const tiboAutoGroupRestoreKey = "codex_tibo_verdict:auto_group_restore"

type tiboGroupMembership struct {
	GroupID   int64     `json:"group_id"`
	Priority  int       `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}

type tiboGroupRestore struct {
	DegradedGroupID int64                 `json:"degraded_group_id"`
	Groups          []tiboGroupMembership `json:"groups"`
}

// The caller holds the account and settings locks. Keep every membership
// removal, restoration marker and scheduler notification in that transaction.
func moveMultiTiboAccountGroups(ctx context.Context, client *dbent.Client, id int64, sample service.TiboAutoGroupSample, settings service.AstraAutoGroupSettings, extra map[string]any) ([]int64, bool, string, error) {
	normalIDs := settings.NormalGroupIDs()
	allIDs := append(slices.Clone(normalIDs), settings.DegradedGroupID)
	slices.Sort(allIDs)
	rows, err := client.QueryContext(ctx, `SELECT id FROM groups WHERE id=ANY($1)
		AND platform='openai' AND status='active' AND deleted_at IS NULL ORDER BY id FOR SHARE`, pq.Array(allIDs))
	if err != nil {
		return nil, false, "", err
	}
	count := 0
	for rows.Next() {
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, "", err
	}
	if count != len(allIDs) {
		return nil, false, "target_unavailable", nil
	}

	rows, err = client.QueryContext(ctx, `SELECT group_id,priority,created_at FROM account_groups
		WHERE account_id=$1 AND group_id=ANY($2) ORDER BY group_id FOR UPDATE`, id, pq.Array(allIDs))
	if err != nil {
		return nil, false, "", err
	}
	links := map[int64]tiboGroupMembership{}
	for rows.Next() {
		var link tiboGroupMembership
		if err = rows.Scan(&link.GroupID, &link.Priority, &link.CreatedAt); err != nil {
			rows.Close()
			return nil, false, "", err
		}
		links[link.GroupID] = link
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, "", err
	}

	var restore tiboGroupRestore
	if marker := extra[tiboAutoGroupRestoreKey]; marker != nil {
		raw, err := json.Marshal(marker)
		if err != nil {
			return nil, false, "", err
		}
		if err := json.Unmarshal(raw, &restore); err != nil {
			return nil, false, "", err
		}
	}
	_, hasDegraded := links[settings.DegradedGroupID]
	if sample.Verdict == "degraded" {
		var sources []tiboGroupMembership
		for _, groupID := range normalIDs {
			if link, ok := links[groupID]; ok {
				sources = append(sources, link)
			}
		}
		if len(sources) == 0 {
			return nil, false, "outside_normal_groups", nil
		}
		// A new quarantine cycle replaces stale history. Repeated degradation
		// preserves earlier sources and records any newly enrolled normal groups.
		if !hasDegraded || restore.DegradedGroupID != settings.DegradedGroupID {
			restore = tiboGroupRestore{DegradedGroupID: settings.DegradedGroupID}
		}
		for _, source := range sources {
			index := slices.IndexFunc(restore.Groups, func(g tiboGroupMembership) bool { return g.GroupID == source.GroupID })
			if index < 0 {
				restore.Groups = append(restore.Groups, source)
			} else {
				restore.Groups[index] = source
			}
		}
		if !hasDegraded {
			first := sources[0]
			_, err = client.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at)
				VALUES($1,$2,$3,$4)`, id, settings.DegradedGroupID, first.Priority, first.CreatedAt)
			if err != nil {
				return nil, false, "", err
			}
		}
		_, err = client.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=ANY($2)`, id, pq.Array(normalIDs))
		if err != nil {
			return nil, false, "", err
		}
		extra[tiboAutoGroupRestoreKey] = restore
		return allIDs, true, "moved", nil
	}

	if !hasDegraded {
		return nil, false, "outside_degraded_group", nil
	}
	if len(restore.Groups) == 0 {
		// Existing single-group installations can recover after saving the new
		// UI. With several possible destinations, never invent prior membership.
		if len(normalIDs) != 1 {
			return nil, false, "original_groups_unknown", nil
		}
		original := links[settings.DegradedGroupID]
		original.GroupID = normalIDs[0]
		restore = tiboGroupRestore{DegradedGroupID: settings.DegradedGroupID, Groups: []tiboGroupMembership{original}}
	}
	if restore.DegradedGroupID != settings.DegradedGroupID {
		return nil, false, "restore_target_changed", nil
	}
	for _, original := range restore.Groups {
		if !slices.Contains(normalIDs, original.GroupID) {
			return nil, false, "restore_groups_changed", nil
		}
	}
	for _, original := range restore.Groups {
		// Preserve a manual edit to an existing normal membership.
		_, err = client.ExecContext(ctx, `INSERT INTO account_groups(account_id,group_id,priority,created_at)
			VALUES($1,$2,$3,$4) ON CONFLICT (account_id,group_id) DO NOTHING`, id, original.GroupID, original.Priority, original.CreatedAt)
		if err != nil {
			return nil, false, "", err
		}
	}
	_, err = client.ExecContext(ctx, `DELETE FROM account_groups WHERE account_id=$1 AND group_id=$2`, id, settings.DegradedGroupID)
	if err != nil {
		return nil, false, "", err
	}
	extra[tiboAutoGroupRestoreKey] = nil
	return allIDs, true, "restored", nil
}
