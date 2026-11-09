package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeyAstraAutoGroup = "astra_auto_group"

type AstraAutoGroupSettings struct {
	Enabled         bool    `json:"enabled"`
	HealthyGroupID  int64   `json:"healthy_group_id"`
	HealthyGroupIDs []int64 `json:"healthy_group_ids,omitempty"`
	DegradedGroupID int64   `json:"degraded_group_id"`
}

// NormalGroupIDs accepts existing single-group settings until they are saved
// through the multi-select UI. An explicitly supplied list takes precedence.
func (s AstraAutoGroupSettings) NormalGroupIDs() []int64 {
	if s.HealthyGroupIDs != nil {
		return slices.Clone(s.HealthyGroupIDs)
	}
	if s.HealthyGroupID > 0 {
		return []int64{s.HealthyGroupID}
	}
	return nil
}

func (s AstraAutoGroupSettings) ValidTargets() bool {
	ids := s.NormalGroupIDs()
	if len(ids) == 0 || len(ids) > 100 || s.DegradedGroupID <= 0 {
		return false
	}
	seen := map[int64]bool{s.DegradedGroupID: true}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *SettingService) GetAstraAutoGroupSettings(ctx context.Context) (*AstraAutoGroupSettings, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyAstraAutoGroup)
	if errors.Is(err, ErrSettingNotFound) {
		return &AstraAutoGroupSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	var settings AstraAutoGroupSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("decode Astra auto-group settings: %w", err)
	}
	return &settings, nil
}

func (s *SettingService) UpdateAstraAutoGroupSettings(ctx context.Context, settings AstraAutoGroupSettings) error {
	invalid := infraerrors.BadRequest("ASTRA_AUTO_GROUP_INVALID", "请选择至少一个正常分组和一个不同的降级分组，分组必须是有效的 OpenAI 分组")
	if settings.HealthyGroupID < 0 || settings.DegradedGroupID < 0 {
		return invalid
	}
	if settings.Enabled {
		if !settings.ValidTargets() {
			return invalid
		}
		if s.defaultSubGroupReader == nil {
			return fmt.Errorf("Astra auto-group group reader unavailable")
		}
		for _, id := range append(settings.NormalGroupIDs(), settings.DegradedGroupID) {
			group, err := s.defaultSubGroupReader.GetByID(ctx, id)
			if errors.Is(err, ErrGroupNotFound) {
				return invalid
			}
			if err != nil {
				return err
			}
			if group == nil || group.Platform != PlatformOpenAI || group.Status != StatusActive {
				return invalid
			}
		}
	}
	// A single setting row keeps the switch and all targets atomic. Disabling
	// remains possible after a selected group is deleted or made inactive.
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyAstraAutoGroup, string(raw))
}

// TiboAutoGroupSample is issued only by a newly completed active Tibo probe.
// StartedAt orders overlapping probes across processes; completion order must
// not let an older request undo a newer result.
type TiboAutoGroupSample struct {
	Verdict     string
	StartedAt   time.Time
	CompletedAt time.Time
}

// TiboAutoGroupRepository persists the observation and its bounded membership
// change atomically. Generic UpdateExtra and passive model observations cannot
// invoke this capability implicitly.
type TiboAutoGroupRepository interface {
	PersistTiboProbeResult(context.Context, int64, map[string]any, TiboAutoGroupSample) error
}
