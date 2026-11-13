package service

import (
	"fmt"
	"maps"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
)

type bulkOpenAISettings struct {
	bps                     bool
	bpsCredentials          bool
	longContextBilling      bool
	endpointCapabilities    bool
	responsesMode           bool
	capabilitiesIncludeChat bool
	forcedResponsesMode     bool
}

func (s bulkOpenAISettings) any() bool {
	return s.bps || s.bpsCredentials || s.longContextBilling || s.endpointCapabilities || s.responsesMode
}

func normalizeBulkOpenAISettings(input *BulkUpdateAccountsInput) (bulkOpenAISettings, error) {
	var settings bulkOpenAISettings
	if input == nil {
		return settings, nil
	}
	stripRemovedOpenAIExcelBPSExtra(input.Extra)
	var err error
	settings.bps, err = normalizeBulkExcelBPSExtra(input.Extra)
	if err != nil {
		return settings, err
	}
	_, settings.bpsCredentials = input.Credentials["plan_type"]
	if _, exists := input.Credentials["client_id"]; exists {
		settings.bps = true
	}

	if _, exists := input.Extra[openAILongContextBillingEnabledKey]; exists {
		settings.longContextBilling = true
		if err := ValidateOpenAILongContextBillingExtra(PlatformOpenAI, input.Extra); err != nil {
			return settings, err
		}
	}

	if raw, exists := input.Credentials[openAIEndpointCapabilitiesCredentialKey]; exists {
		settings.endpointCapabilities = true
		capabilities, includeChat, err := normalizeBulkOpenAIEndpointCapabilities(raw)
		if err != nil {
			return settings, err
		}
		settings.capabilitiesIncludeChat = includeChat
		input.Credentials[openAIEndpointCapabilitiesCredentialKey] = capabilities
	}

	if raw, exists := input.Extra[openai_compat.ExtraKeyResponsesMode]; exists {
		settings.responsesMode = true
		mode, forced, err := normalizeBulkOpenAIResponsesMode(raw)
		if err != nil {
			return settings, err
		}
		settings.forcedResponsesMode = forced
		input.Extra[openai_compat.ExtraKeyResponsesMode] = mode
	}

	if settings.endpointCapabilities && !settings.capabilitiesIncludeChat {
		if settings.forcedResponsesMode {
			return settings, infraerrors.BadRequest(
				"OPENAI_RESPONSES_MODE_INVALID",
				"a forced Responses route requires the chat_completions endpoint capability",
			)
		}
		if input.Extra == nil {
			input.Extra = make(map[string]any, 1)
		}
		input.Extra[openai_compat.ExtraKeyResponsesMode] = nil
		settings.responsesMode = true
	}

	return settings, nil
}

func stripRemovedOpenAIExcelBPSExtra(extra map[string]any) {
	if extra == nil {
		return
	}
	for _, key := range []string{
		"codex_tibo_verdict:bps",
	} {
		delete(extra, key)
	}
}

// A nil model scope removes the key (all models); an empty list selects no models.
func normalizeBulkExcelBPSExtra(extra map[string]any) (bool, error) {
	changed := false
	if _, exists := extra[ExcelBPS403RecoveryIntervalMinutesKey]; exists {
		changed = true
		if err := validateExcelBPS403RecoveryExtra(extra); err != nil {
			return true, err
		}
	}
	for _, key := range []string{"openai_excel_bps", ExcelBPSIgnoreEncryptedContentKey, "openai_excel_bps_cache_creation_as_input", "openai_excel_bps_auto_disable_on_403", ExcelBPSAutoRecoverOn403Key, ExcelBPSAutoMoveOn403Key, "openai_excel_bps_mihomo", OpenAIExcelBPSSessionProxyKey, ExcelBPSOmitUnsupportedToolsKey} {
		if raw, exists := extra[key]; exists {
			changed = true
			if _, ok := raw.(bool); !ok {
				return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", key+" must be a boolean")
			}
		}
	}
	if raw, exists := extra[ExcelBPS403TargetGroupIDKey]; exists {
		changed = true
		if _, ok := excelBPS403GroupID(raw); raw != nil && !ok {
			return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", ExcelBPS403TargetGroupIDKey+" must be a nonnegative integer")
		}
	}
	if raw, exists := extra["openai_excel_bps_models"]; exists {
		changed = true
		if raw != nil {
			models := make([]string, 0)
			switch values := raw.(type) {
			case []string:
				models = append(models, values...)
			case []any:
				for _, value := range values {
					model, ok := value.(string)
					if !ok {
						return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", "openai_excel_bps_models must be an array of strings or null")
					}
					models = append(models, model)
				}
			default:
				return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", "openai_excel_bps_models must be an array of strings or null")
			}
			normalized := make([]string, 0, len(models))
			seen := make(map[string]bool, len(models))
			for _, model := range models {
				model = strings.TrimSpace(model)
				if model != "" && !seen[model] {
					normalized = append(normalized, model)
					seen[model] = true
				}
			}
			extra["openai_excel_bps_models"] = normalized
		}
	}
	if enabled, exists := extra["openai_excel_bps"].(bool); exists && !enabled {
		extra["openai_excel_bps_models"] = nil
		if _, exists := extra["openai_excel_bps_mihomo"]; exists {
			extra["openai_excel_bps_mihomo"] = false
		}
		extra["openai_excel_bps_cache_creation_as_input"] = false
		if _, exists := extra[ExcelBPSAutoRecoverOn403Key]; exists {
			extra[ExcelBPSAutoRecoverOn403Key] = false
		}
		if _, exists := extra[ExcelBPSIgnoreEncryptedContentKey]; exists {
			extra[ExcelBPSIgnoreEncryptedContentKey] = false
		}
		if _, exists := extra["openai_excel_bps_auto_disable_on_403"]; exists {
			extra["openai_excel_bps_auto_disable_on_403"] = false
		}
		if _, exists := extra[ExcelBPSAutoMoveOn403Key]; exists {
			extra[ExcelBPSAutoMoveOn403Key] = false
		}
		if _, exists := extra[ExcelBPS403TargetGroupIDKey]; exists {
			extra[ExcelBPS403TargetGroupIDKey] = nil
		}
	}
	if raw, exists := extra[ExcelBPSProxySourceKey]; exists {
		changed = true
		if raw != ExcelBPSProxySourceMihomo && raw != ExcelBPSProxySourceIPPool {
			return true, infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", "BPS proxy source must be mihomo or ip_pool")
		}
	}
	if extra["openai_excel_bps"] == false {
		if _, exists := extra[OpenAIExcelBPSSessionProxyKey]; exists {
			extra[OpenAIExcelBPSSessionProxyKey] = false
		}
	}
	return changed, nil
}

func normalizeBulkOpenAIEndpointCapabilities(raw any) (any, bool, error) {
	if raw == nil {
		return nil, true, nil
	}

	values := make([]string, 0, 2)
	switch typed := raw.(type) {
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, false, invalidBulkOpenAIEndpointCapabilities()
			}
			values = append(values, value)
		}
	case []string:
		values = append(values, typed...)
	default:
		return nil, false, invalidBulkOpenAIEndpointCapabilities()
	}

	selected := make(map[string]bool, 2)
	for _, value := range values {
		switch OpenAIEndpointCapability(value) {
		case OpenAIEndpointCapabilityChatCompletions, OpenAIEndpointCapabilityEmbeddings:
			selected[value] = true
		default:
			return nil, false, invalidBulkOpenAIEndpointCapabilities()
		}
	}
	if len(selected) == 0 {
		return nil, false, invalidBulkOpenAIEndpointCapabilities()
	}

	includeChat := selected[string(OpenAIEndpointCapabilityChatCompletions)]
	if includeChat && selected[string(OpenAIEndpointCapabilityEmbeddings)] {
		return nil, true, nil
	}
	if includeChat {
		return []string{string(OpenAIEndpointCapabilityChatCompletions)}, true, nil
	}
	return []string{string(OpenAIEndpointCapabilityEmbeddings)}, false, nil
}

func invalidBulkOpenAIEndpointCapabilities() error {
	return infraerrors.BadRequest(
		"OPENAI_ENDPOINT_CAPABILITIES_INVALID",
		"openai_capabilities must contain chat_completions, embeddings, or both",
	)
}

func normalizeBulkOpenAIResponsesMode(raw any) (any, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	mode, ok := raw.(string)
	if !ok {
		return nil, false, invalidBulkOpenAIResponsesMode()
	}
	switch openai_compat.ResponsesSupportMode(mode) {
	case openai_compat.ResponsesSupportModeAuto:
		return nil, false, nil
	case openai_compat.ResponsesSupportModeForceResponses,
		openai_compat.ResponsesSupportModeForceChatCompletions:
		return mode, true, nil
	default:
		return nil, false, invalidBulkOpenAIResponsesMode()
	}
}

func invalidBulkOpenAIResponsesMode() error {
	return infraerrors.BadRequest(
		"OPENAI_RESPONSES_MODE_INVALID",
		"openai_responses_mode must be auto, force_responses, force_chat_completions, or null",
	)
}

func validateBulkOpenAISettingsTargets(
	input *BulkUpdateAccountsInput,
	settings bulkOpenAISettings,
	targetsByID map[int64]*Account,
) error {
	if input == nil || !settings.any() {
		return nil
	}

	for _, accountID := range input.AccountIDs {
		account, ok := targetsByID[accountID]
		if !ok || account == nil {
			return invalidBulkOpenAITarget(accountID, "account does not exist")
		}

		if settings.bps || (settings.bpsCredentials && account.Extra[OpenAIExcelBPSEnabledKey] == true) {
			if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth || account.IsOpenAIAgentIdentity() || account.IsOpenAIPersonalAccessToken() {
				return invalidBulkOpenAITarget(accountID, "BPS settings require an OpenAI ChatGPT OAuth account")
			}
			candidate := *account
			candidate.Extra = maps.Clone(account.Extra)
			if candidate.Extra == nil {
				candidate.Extra = make(map[string]any)
			}
			for key, value := range input.Extra {
				candidate.Extra[key] = value
			}
			// Bulk persistence merges JSONB keys; validate that same effective
			// credential set, not the full-object PUT replacement semantics.
			candidate.Credentials = maps.Clone(account.Credentials)
			if candidate.Credentials == nil {
				candidate.Credentials = make(map[string]any)
			}
			for key, value := range input.Credentials {
				candidate.Credentials[key] = value
			}
			if err := validateOpenAIBPSSettings(&candidate); err != nil {
				return err
			}
		}
		if settings.longContextBilling {
			if account.Platform != PlatformOpenAI || !supportsOpenAILongContextBilling(account.Type) {
				return invalidBulkOpenAITarget(accountID, "long-context billing requires an OpenAI OAuth, setup-token, or API-key account")
			}
		}

		if settings.endpointCapabilities || settings.responsesMode {
			if account.Platform != PlatformOpenAI || account.Type != AccountTypeAPIKey {
				return invalidBulkOpenAITarget(accountID, "endpoint capabilities and Responses routing require an OpenAI API-key account")
			}
		}

		if settings.forcedResponsesMode && !settings.capabilitiesIncludeChat &&
			!settings.endpointCapabilities &&
			!account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions) {
			return invalidBulkOpenAITarget(accountID, "a forced Responses route requires the chat_completions endpoint capability")
		}
	}

	return nil
}

func supportsOpenAILongContextBilling(accountType string) bool {
	switch accountType {
	case AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey:
		return true
	default:
		return false
	}
}

func invalidBulkOpenAITarget(accountID int64, message string) error {
	return infraerrors.BadRequest(
		"OPENAI_BULK_TARGET_INVALID",
		fmt.Sprintf("account %d: %s", accountID, message),
	).WithMetadata(map[string]string{"account_id": strconv.FormatInt(accountID, 10)})
}
