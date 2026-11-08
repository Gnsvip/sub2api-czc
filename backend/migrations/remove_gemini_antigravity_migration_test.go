package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGooglePlatformMigrationsPreserveExistingData(t *testing.T) {
	for _, name := range []string{"233_remove_gemini_antigravity.sql", "237_add_minimax_platform.sql", "241_opencode_go_platform.sql", "274_restore_google_platforms.sql"} {
		t.Run(name, func(t *testing.T) {
			content, err := FS.ReadFile(name)
			require.NoError(t, err)
			sql := strings.ToUpper(strings.Join(strings.Fields(string(content)), " "))
			for _, destructive := range []string{"DELETE FROM", "UPDATE ACCOUNTS", "UPDATE GROUPS", "UPDATE SETTINGS", "UPDATE CHANNEL_MONITOR", "TRUNCATE", "DROP TABLE"} {
				require.NotContains(t, sql, destructive)
			}
			if name != "233_remove_gemini_antigravity.sql" {
				for _, line := range strings.Split(string(content), "\n") {
					if strings.Contains(line, "CHECK (platform IN") || strings.Contains(line, "CHECK (provider IN") || strings.Contains(line, "CHECK (target_platform IN") {
						require.Contains(t, line, "'gemini'")
						require.Contains(t, line, "'antigravity'")
					}
				}
			}
		})
	}
}
