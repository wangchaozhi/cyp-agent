package app

import (
	"context"
	"testing"

	"github.com/wangchaozhi/cyp-agent/internal/config"
)

func TestNewDegradesWhenOHLCVArchiveIsUnavailableAndLoggerIsNil(t *testing.T) {
	settings := config.DefaultSettings()
	settings.Persistence = "memory"
	settings.DBURL = "postgresql://cyp:cyp@127.0.0.1:1/cyp?connect_timeout=1"
	settings.OHLCVArchiveEnabled = true
	settings.RuntimeAutostart = false
	settings.Automation.Enabled = false

	application, err := New(context.Background(), settings, "", nil)
	if err != nil {
		t.Fatalf("archive outage should not stop the application: %v", err)
	}
	application.Close()
}
