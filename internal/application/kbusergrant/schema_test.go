package kbusergrant_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir)
		dir = parent
	}
}

func TestKBUserGrantMigrationContract(t *testing.T) {
	root := moduleRoot(t)
	for _, rel := range []string{
		"migrations/versioned/000137_kb_user_grants.up.sql",
		"migrations/sqlite/000053_kb_user_grants.up.sql",
	} {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, rel))
			require.NoError(t, err)
			text := strings.ToLower(string(body))
			require.Contains(t, text, "primary key (knowledge_base_id, recipient_user_id)")
			require.Contains(t, text, "check (permission in ('viewer', 'editor'))")
			require.NotContains(t, text, "deleted_at")
		})
	}
	for _, rel := range []string{
		"migrations/versioned/000137_kb_user_grants.down.sql",
		"migrations/sqlite/000053_kb_user_grants.down.sql",
	} {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, rel))
			require.NoError(t, err)
			text := strings.ToLower(string(body))
			require.Contains(t, text, "drop table")
			require.Contains(t, text, "kb_user_grants")
			require.NotContains(t, text, "kb_shares")
		})
	}
}
