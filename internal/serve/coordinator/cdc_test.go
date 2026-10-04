package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/storage"
)

// Without FORGE_CDC_TRIGGERS nothing starts — the default stays untouched.
func TestSetupCDCOffByDefault(t *testing.T) {
	t.Setenv(envCDCTriggers, "")

	coord := coordinator.NewCoordinator(nil)
	stop, err := setupCDC(context.Background(), coord)
	require.NoError(t, err)
	stop()
}

// Configured without a source database: a loud error instead of a silently
// non-capturing CDC.
func TestSetupCDCRequiresSourceDSN(t *testing.T) {
	t.Setenv(envCDCTriggers, filepath.Join(t.TempDir(), "triggers.yaml"))
	t.Setenv(envPGDSNForCDC, "") // CI sets it for other tests; this one must not see it

	coord := coordinator.NewCoordinator(nil)
	_, err := setupCDC(context.Background(), coord)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FORGE_PG_DSN")
}

// The full promised chain against a real database: INSERT → WAL replication
// event → trigger condition → params mapping → bridge → SubmitDAG →
// workflow instance with the mapped input in storage. Gated on a logical
// wal_level (CI sets POSTGRES_INITDB_ARGS; local boxes without it skip).
func TestCDCTriggerChainIntegration(t *testing.T) {
	dsn := os.Getenv("FORGE_PG_DSN")
	if dsn == "" {
		t.Skip("FORGE_PG_DSN not set; runs in CI with the postgres service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pgStore, err := storage.NewPGStorage(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pgStore.Close() })

	// wal_level gate: logical replication needs it; report honestly instead
	// of failing with a confusing replication error.
	walLevel := queryScalar(t, ctx, pgStore, "SHOW wal_level")
	if walLevel != "logical" {
		t.Skipf("wal_level=%s (need logical); CI sets POSTGRES_INITDB_ARGS", walLevel)
	}

	// Fixture table (rerunnable: created if missing, emptied per run).
	execSQL(t, ctx, pgStore, "CREATE TABLE IF NOT EXISTS cdc_chain_test (id int primary key, note text)")
	execSQL(t, ctx, pgStore, "DELETE FROM cdc_chain_test")

	// Trigger definition: one CDC trigger on the fixture table.
	triggersPath := filepath.Join(t.TempDir(), "triggers.yaml")
	triggersYAML := `
triggers:
  - name: chain_test
    type: cdc
    source:
      type: postgres
      table: cdc_chain_test
      events: [INSERT]
    workflow: cdc_demo
    params_mapping:
      item_id: "{{.new.id}}"
      note: "{{.new.note}}"
`
	require.NoError(t, os.WriteFile(triggersPath, []byte(triggersYAML), 0o644))

	// Workflow definition (registry dialect — the file the submitter reads).
	workflowsDir := t.TempDir()
	workflowYAML := `
apiVersion: forge/v1
kind: Workflow
metadata:
  name: cdc_demo
  version: "1.0"
stages:
  - name: only
    tasks:
      - worker: shell
        action: run
        params:
          command: "echo captured"
        output: out
`
	require.NoError(t, os.WriteFile(filepath.Join(workflowsDir, "cdc_demo.yaml"), []byte(workflowYAML), 0o644))

	t.Setenv(envCDCTriggers, triggersPath)
	t.Setenv(envWorkflowsDir, workflowsDir)
	t.Setenv(envCDCMode, "wal")
	t.Setenv(envCDCPublication, "cdc_chain_pub")
	t.Setenv(envPGReplDSN, "") // derived from FORGE_PG_DSN

	boltStore, err := storage.NewBoltStorage(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { boltStore.Close() })

	coord := coordinator.NewCoordinator(boltStore)
	stop, err := setupCDC(ctx, coord)
	require.NoError(t, err)
	defer stop()

	// The WAL source subscribes asynchronously (slot + publication are
	// created inside Subscribe), so the first insert may land too early —
	// keep re-inserting until the chain has fired.
	var workflowID string
	require.Eventually(t, func() bool {
		execSQL(t, ctx, pgStore, "DELETE FROM cdc_chain_test")
		execSQL(t, ctx, pgStore, "INSERT INTO cdc_chain_test VALUES (1, 'first')")

		workflows, err := boltStore.ListWorkflows(ctx, "", 100, 0)
		if err != nil {
			return false
		}
		for _, wf := range workflows {
			if wf.Name == "cdc_demo" {
				workflowID = wf.ID
				var input map[string]any
				if json.Unmarshal(wf.Input, &input) == nil && fmt.Sprint(input["item_id"]) == "1" {
					return true
				}
			}
		}
		return false
	}, 45*time.Second, 3*time.Second,
		"CDC event should reach the trigger and submit cdc_demo with mapped params")

	// The submitted instance went through bridge + SubmitDAG: its tasks are
	// the bridged registry tasks, ready for dispatch.
	tasks, err := boltStore.ListTasksByWorkflow(ctx, workflowID)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "shell", tasks[0].Handler, "worker became handler through the bridge")
	assert.Contains(t, string(tasks[0].Input), "echo captured")
}

func queryScalar(t *testing.T, ctx context.Context, pg *storage.PGStorage, query string) string {
	t.Helper()
	rows, err := pg.Pool().Query(ctx, query)
	require.NoError(t, err)
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no rows from %q", query)
	}
	values, err := rows.Values()
	require.NoError(t, err)
	return fmt.Sprint(values[0])
}

func execSQL(t *testing.T, ctx context.Context, pg *storage.PGStorage, sql string) {
	t.Helper()
	_, err := pg.Pool().Exec(ctx, sql)
	require.NoError(t, err, "exec: %s", sql)
}
