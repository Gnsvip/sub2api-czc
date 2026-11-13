package service

import (
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const OpenAIExcelBPSEnabledKey = "openai_excel_bps"
const OpenAIExcelBPSModelsKey = "openai_excel_bps_models"
const OpenAIExcelBPSSessionProxyKey = "openai_excel_bps_session_proxy"

func (a *Account) isBPSCredential() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Type == AccountTypeOAuth && a.GetCredential("client_id") == openai.ExcelClientID
}

func (a *Account) isBPSEnabled() bool { return a.IsExcelBPSEnabled() }

func (a *Account) bpsModelAllowed(model string) bool {
	models, err := bpsModelList(a.Extra[OpenAIExcelBPSModelsKey])
	if err != nil {
		return false
	}
	if len(models) == 0 {
		_, configured := a.Extra[OpenAIExcelBPSModelsKey]
		return !configured
	}
	for _, candidate := range models {
		if model == candidate {
			return true
		}
	}
	return false
}

func bpsModelList(raw any) ([]string, error) {
	var models []string
	switch v := raw.(type) {
	case nil:
	case []string:
		models = v
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("BPS models must be strings")
			}
			models = append(models, s)
		}
	default:
		return nil, fmt.Errorf("BPS models must be an array")
	}
	if len(models) > 64 {
		return nil, fmt.Errorf("BPS supports at most 64 configured models")
	}
	for _, model := range models {
		if model == "" || len(model) > 128 || strings.TrimSpace(model) != model {
			return nil, fmt.Errorf("invalid BPS model name")
		}
	}
	return models, nil
}

func validateOpenAIBPSSettings(account *Account) error {
	if p, err := ParseExcelBPSAutoPolicy(account.Extra[ExcelBPSAutoPolicyKey]); err != nil {
		return infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", err.Error())
	} else if p != nil && p.Enabled && !QualityBPSEligible(account) {
		return infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", "Automatic BPS requires an eligible OAuth account")
	}

	delete(account.Extra, ExcelBPSCredentialStateExtraKey)
	if raw, exists := account.Extra[OpenAIExcelBPSModelsKey]; exists && raw == nil {
		delete(account.Extra, OpenAIExcelBPSModelsKey)
	}
	invalid := func(message string) error { return infraerrors.BadRequest("OPENAI_EXCEL_BPS_INVALID", message) }
	for _, key := range []string{OpenAIExcelBPSEnabledKey, OpenAIExcelBPSSessionProxyKey, "openai_excel_bps_mihomo", "openai_excel_bps_cache_creation_as_input", "openai_excel_bps_auto_disable_on_403", "openai_excel_bps_auto_move_on_403", ExcelBPSAutoRecoverOn403Key, "openai_excel_bps_ignore_encrypted_content", "openai_excel_bps_omit_unsupported_tools"} {
		if raw, exists := account.Extra[key]; exists {
			if _, ok := raw.(bool); !ok {
				return invalid(key + " must be a boolean")
			}
		}
	}
	if _, err := bpsModelList(account.Extra[OpenAIExcelBPSModelsKey]); err != nil {
		return invalid(err.Error())
	}
	enabled, _ := account.Extra[OpenAIExcelBPSEnabledKey].(bool)
	pooled, _ := account.Extra[OpenAIExcelBPSSessionProxyKey].(bool)
	if enabled && (!QualityBPSEligible(account) || account.IsOpenAIPersonalAccessToken()) {
		return invalid("BPS requires a non-free OpenAI ChatGPT OAuth account")
	}
	if pooled && !enabled {
		return invalid("BPS session proxy requires BPS to be enabled")
	}

	if raw, exists := account.Extra[ExcelBPSProxySourceKey]; exists && raw != "mihomo" && raw != "ip_pool" {
		return invalid("BPS proxy source must be mihomo or ip_pool")
	}
	if err := validateExcelBPS403RecoveryExtra(account.Extra); err != nil {
		return err
	}
	return validateExcelBPS403GroupExtra(account.Extra)
}
