//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func bpsEnabledTestSettings() *SettingService {
	return &SettingService{settingRepo: &settingRepoStub{values: map[string]string{SettingKeyExcelBPSEnabled: "true"}}}
}
func TestBPSGlobalDefaultRemainsDisabled(t *testing.T) {
	s := &OpenAIGatewayService{}
	require.False(t, s.excelBPSGloballyEnabled(context.Background()))
	s.settingService = &SettingService{settingRepo: &settingRepoStub{}}
	require.False(t, s.excelBPSGloballyEnabled(context.Background()))
	s.settingService = bpsEnabledTestSettings()
	require.True(t, s.excelBPSGloballyEnabled(context.Background()))
}

// Production defaults remain disabled. Protocol regressions explicitly enable
// the feature while reusing the existing native gateway fixtures.
func excelBPSTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	s := openAIClientToolsTestService(upstream)
	if s.settingService == nil {
		s.settingService = &SettingService{}
	}
	s.settingService.settingRepo = &settingRepoStub{values: map[string]string{SettingKeyExcelBPSEnabled: "true"}}
	return s
}
