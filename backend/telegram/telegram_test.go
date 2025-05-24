// Package telegram_test contains tests for the telegram backend.
package telegram_test

import (
	"testing"
	"github.com/rclone/rclone/backend/telegram"
	"github.com/rclone/rclone/fstest"
)

func TestIntegration(t *testing.T) {
	fstest.TestBackend(t, &telegram.Fs{})
}
