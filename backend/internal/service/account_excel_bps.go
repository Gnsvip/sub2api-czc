package service

import "strings"

// IsExcelBPSEnabled routes an existing ChatGPT OAuth account to the Excel gateway.
// An independent Excel grant supplies BPS credentials; native OAuth is preserved.
func (a *Account) IsExcelBPSEnabled() bool {
	if a == nil || a.excelBPSRoutingDisabled || a.Platform != PlatformOpenAI || a.Type != AccountTypeOAuth || a.IsOpenAIAgentIdentity() || a.IsOpenAIPersonalAccessToken() {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(a.GetCredential("plan_type")), "free") {
		return false
	}
	enabled, _ := a.Extra["openai_excel_bps"].(bool)
	return enabled && a.Extra[ExcelBPSAuthorizationPendingKey] != true
}

// The requested switch stays on while the worker prepares the independent grant.
const ExcelBPSAuthorizationPendingKey = "openai_excel_bps_authorization_pending"

const ExcelBPSIgnoreEncryptedContentKey = "openai_excel_bps_ignore_encrypted_content"

// IsExcelBPSIgnoreEncryptedContentEnabled opts into replacing ciphertext that
// BPS cannot forward, such as sub-agent messages in an old Codex conversation,
// with an omission notice instead of rejecting the whole request.
func (a *Account) IsExcelBPSIgnoreEncryptedContentEnabled() bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	enabled, _ := a.Extra[ExcelBPSIgnoreEncryptedContentKey].(bool)
	return enabled
}

func (a *Account) IsExcelBPSMihomoEnabled() bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	enabled, _ := a.Extra["openai_excel_bps_mihomo"].(bool)
	legacy, _ := a.Extra[OpenAIExcelBPSSessionProxyKey].(bool)
	return enabled || legacy
}

// ExcelBPSProxySourceKey selects the pool behind the BPS session proxy toggle:
// the managed Mihomo kernel (default) or the admin proxy list (IP 绠＄悊).
const ExcelBPSProxySourceKey = "openai_excel_bps_proxy_source"

const (
	ExcelBPSProxySourceMihomo = "mihomo"
	ExcelBPSProxySourceIPPool = "ip_pool"
)

// ExcelBPSProxySource is meaningful only while the session proxy is enabled;
// unknown stored values fall back to the Mihomo pool rather than direct.
func (a *Account) ExcelBPSProxySource() string {
	if !a.IsExcelBPSMihomoEnabled() {
		return ""
	}
	source, _ := a.Extra[ExcelBPSProxySourceKey].(string)
	if source == "" && a.Extra[OpenAIExcelBPSSessionProxyKey] == true {
		return ExcelBPSProxySourceIPPool
	}
	if source == ExcelBPSProxySourceIPPool {
		return ExcelBPSProxySourceIPPool
	}
	return ExcelBPSProxySourceMihomo
}

// IsExcelBPSCacheCreationAsInputEnabled controls local billing and downstream usage.
// The setting has no effect unless this account uses the Excel/BPS protocol.
func (a *Account) IsExcelBPSCacheCreationAsInputEnabled() bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	enabled, _ := a.Extra["openai_excel_bps_cache_creation_as_input"].(bool)
	return enabled
}

// IsExcelBPSAutoDisableOn403Enabled opts into disabling BPS after a generic 403.
func (a *Account) IsExcelBPSAutoDisableOn403Enabled() bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	enabled, _ := a.Extra["openai_excel_bps_auto_disable_on_403"].(bool)
	return enabled
}

// isExcelBPSAllModelsEnabled preserves legacy account-wide routing. An explicit
// list, including an empty or malformed list, never enables BPS for all models.
func (a *Account) isExcelBPSAllModelsEnabled() bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	_, scoped := a.Extra["openai_excel_bps_models"]
	return !scoped
}

// IsExcelBPSEnabledForModel selects the protocol after account model mapping.
// The list selects a protocol; it does not restrict access to other models.
func (a *Account) IsExcelBPSEnabledForModel(requestedModel string) bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	return a.isExcelBPSUpstreamModelEnabled(a.getExcelBPSMappedModel(requestedModel))
}

// IsExcelBPSImagesEnabledForModel routes image generation through BPS only
// when the image model is listed explicitly. Legacy account-wide routing keeps
// images on Codex so an upgrade does not silently change the image channel.
func (a *Account) IsExcelBPSImagesEnabledForModel(requestedModel string) bool {
	if !a.IsExcelBPSEnabled() || a.isExcelBPSAllModelsEnabled() {
		return false
	}
	model := a.getExcelBPSMappedModel(requestedModel)
	return usesCodexDirectImages(model) && excelBPSImagesSupportedModel(model) && a.isExcelBPSUpstreamModelEnabled(model)
}

func (a *Account) isExcelBPSUpstreamModelEnabled(model string) bool {
	if !a.IsExcelBPSEnabled() {
		return false
	}
	raw, scoped := a.Extra["openai_excel_bps_models"]
	if !scoped {
		return true
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	switch models := raw.(type) {
	case []string:
		for _, selected := range models {
			if strings.TrimSpace(selected) == model {
				return true
			}
		}
	case []any:
		for _, selected := range models {
			if name, ok := selected.(string); ok && strings.TrimSpace(name) == model {
				return true
			}
		}
	}
	return false
}

// BPS retains explicit upstream aliases without changing native Codex mapping.
func (a *Account) excelBPSModelMapping() map[string]string {
	if a == nil || !a.IsExcelBPSEnabled() {
		return nil
	}
	return stringMappingFromRaw(a.Credentials["model_mapping"])
}
func (a *Account) getExcelBPSMappedModel(model string) string {
	if mapped, ok := resolveRequestedModelInMapping(a.excelBPSModelMapping(), model); ok {
		return mapped
	}
	return model
}
