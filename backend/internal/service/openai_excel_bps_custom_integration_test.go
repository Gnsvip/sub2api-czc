//go:build unit

package service

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
