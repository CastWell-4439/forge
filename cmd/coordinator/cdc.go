package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/castwell/forge/internal/cdc"
	"github.com/castwell/forge/internal/coordinator"
	"github.com/castwell/forge/internal/registry"
)

// Environment variables that configure Change Data Capture.
//
//	FORGE_CDC_TRIGGERS    path to a triggers YAML (triggers: [...]); unset = CDC off
//	FORGE_WORKFLOWS_DIR   workflow definitions the triggers submit (default workflows)
//	FORGE_CDC_MODE        wal (default, real logical replication) | poll (slot-peek legacy)
//	FORGE_PG_REPL_DSN     replication DSN for wal mode (default: FORGE_PG_DSN + replication=database)
//	FORGE_CDC_PUBLICATION publication name (default forge_pub; auto-created)
const (
	envCDCTriggers     = "FORGE_CDC_TRIGGERS"
	envCDCMode         = "FORGE_CDC_MODE"
	envPGReplDSN       = "FORGE_PG_REPL_DSN"
	envCDCPublication  = "FORGE_CDC_PUBLICATION"
	envWorkflowsDir    = "FORGE_WORKFLOWS_DIR"
	envPGDSNForCDC     = "FORGE_PG_DSN"
	defaultWorkflows   = "workflows"
	defaultPublication = "forge_pub"
)

// setupCDC wires CDC triggers when configured and returns a cleanup the
// caller must run at shutdown. With FORGE_CDC_TRIGGERS unset it is a no-op —
// an unconfigured coordinator behaves exactly as before.
//
// The source connects through FORGE_PG_DSN (it monitors a database, which
// need not be the workflow store); the chain assembled is: source (WAL or
// polling) → TriggerManager condition match + params mapping → submitter
// (registry file by name → bridge → SubmitDAG) → workflow instance in
// storage.
func setupCDC(ctx context.Context, coord *coordinator.Coordinator) (func(), error) {
	noop := func() {}
	triggersPath := strings.TrimSpace(os.Getenv(envCDCTriggers))
	if triggersPath == "" {
		return noop, nil
	}
	dsn := strings.TrimSpace(os.Getenv(envPGDSNForCDC))
	if dsn == "" {
		return nil, fmt.Errorf("CDC requires %s (the source database)", envPGDSNForCDC)
	}

	raw, err := os.ReadFile(triggersPath)
	if err != nil {
		return nil, fmt.Errorf("read CDC triggers %s: %w", triggersPath, err)
	}
	triggerSet, err := cdc.ParseTriggerConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("parse CDC triggers: %w", err)
	}

	workflowsDir := envOrDefault(envWorkflowsDir, defaultWorkflows)
	reg := registry.NewRegistry()
	if err := reg.Load(workflowsDir); err != nil {
		return nil, fmt.Errorf("load workflows from %s: %w", workflowsDir, err)
	}

	submitter := func(ctx context.Context, name string, params map[string]any) error {
		cw, err := reg.Get(name)
		if err != nil {
			return fmt.Errorf("cdc submit: workflow %q: %w", name, err)
		}
		dag, err := bridgeDAG(cw)
		if err != nil {
			return err
		}
		input, err := renderCDCInputs(cw, params)
		if err != nil {
			return err
		}
		_, err = coord.SubmitDAG(ctx, dag, input)
		return err
	}

	// The polling source runs its queries through a pool of its own; WAL
	// mode opens a replication connection per source and never needs it.
	var pollPool *pgxpool.Pool

	tm := cdc.NewTriggerManager(submitter)
	sourceFactory := func(src cdc.TriggerSource) (cdc.Source, error) {
		source, err := newCDCSource(ctx, src, dsn, &pollPool)
		if err != nil {
			return nil, err
		}
		return source, nil
	}
	if err := tm.LoadConfig(triggerSet, sourceFactory); err != nil {
		return nil, err
	}
	if tm.TriggerCount() == 0 {
		log.Printf("WARN: %s has no cdc triggers; nothing started", triggersPath)
		return noop, nil
	}

	tm.Start(ctx)
	mode := envOrDefault(envCDCMode, "wal")
	log.Printf("INFO: cdc triggers enabled (count=%d from %s mode=%s)", tm.TriggerCount(), triggersPath, mode)

	var once sync.Once
	return func() {
		once.Do(func() {
			tm.Stop()
			if pollPool != nil {
				pollPool.Close()
			}
		})
	}, nil
}

// newCDCSource builds one trigger's source. wal (default): PGWALSource —
// real logical replication with auto slot and auto publication. poll: the
// slot-peek polling source. Both paths talk to logical decoding (see D-19
// for the honest gap against README's "WAL 不可用时降级" promise); a typo'd
// mode or missing DSN fails loudly instead of silently not capturing.
func newCDCSource(ctx context.Context, src cdc.TriggerSource, dsn string, pollPool **pgxpool.Pool) (cdc.Source, error) {
	if src.Type != "" && src.Type != "postgres" {
		return nil, fmt.Errorf("unsupported CDC source type %q (postgres only)", src.Type)
	}
	if strings.TrimSpace(src.Table) == "" {
		return nil, fmt.Errorf("CDC source requires a table")
	}

	cfg := cdc.SourceConfig{
		Type:     "postgres",
		Table:    src.Table,
		Events:   cdcOperations(src.Events),
		Filter:   src.Filter,
		SlotName: cdcSlotName(src.Table),
	}

	switch strings.ToLower(strings.TrimSpace(os.Getenv(envCDCMode))) {
	case "", "wal":
		repl := strings.TrimSpace(os.Getenv(envPGReplDSN))
		if repl == "" {
			repl = withReplication(dsn)
		}
		publication := envOrDefault(envCDCPublication, defaultPublication)
		return cdc.NewPGWALSource(repl, cfg, publication, cdc.WithAutoCreateSlot(true)), nil
	case "poll":
		if *pollPool == nil {
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				return nil, fmt.Errorf("cdc poll: connect: %w", err)
			}
			*pollPool = pool
		}
		return cdc.NewPGCDCSource(dsn, cfg, cdc.WithQueryFunc(pgQueryFunc(*pollPool))), nil
	default:
		mode := os.Getenv(envCDCMode)
		return nil, fmt.Errorf("unknown %s %q (want wal or poll)", envCDCMode, mode)
	}
}

// renderCDCInputs turns mapped event params into the workflow's input JSON:
// raw params first, then the workflow's inputs block rendered against
// {"event": params} on top (declared inputs win on collision).
func renderCDCInputs(cw *registry.CompiledWorkflow, eventParams map[string]any) (json.RawMessage, error) {
	merged := make(map[string]any, len(eventParams)+len(cw.Inputs))
	for k, v := range eventParams {
		merged[k] = v
	}
	if len(cw.Inputs) > 0 {
		rendered, err := registry.RenderInputs(cw.Inputs, registry.TemplateContext{"event": eventParams})
		if err != nil {
			return nil, fmt.Errorf("render inputs of workflow %q: %w", cw.Name, err)
		}
		for k, v := range rendered {
			merged[k] = v
		}
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow input: %w", err)
	}
	return body, nil
}

// cdcOperations converts the trigger's string event list to source operations.
func cdcOperations(events []string) []cdc.Operation {
	if len(events) == 0 {
		return nil
	}
	ops := make([]cdc.Operation, 0, len(events))
	for _, e := range events {
		ops = append(ops, cdc.Operation(strings.ToUpper(strings.TrimSpace(e))))
	}
	return ops
}

// cdcSlotName derives a per-table replication slot name. Slot names accept
// only lowercase letters, digits and underscore, so anything else is folded
// into "_"; each table gets its own slot so two triggers never compete for
// the same change stream.
func cdcSlotName(table string) string {
	var b strings.Builder
	b.WriteString("cdc_")
	for _, r := range strings.ToLower(table) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// withReplication appends replication=database to a plain DSN.
func withReplication(dsn string) string {
	if strings.Contains(dsn, "replication=") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "replication=database"
}

// pgQueryFunc adapts the storage pool to the polling source's query hook
// (the source itself never opens connections).
func pgQueryFunc(pool *pgxpool.Pool) func(ctx context.Context, query string) ([]map[string]any, error) {
	return func(ctx context.Context, query string) ([]map[string]any, error) {
		rows, err := pool.Query(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		descs := rows.FieldDescriptions()
		var out []map[string]any
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				return nil, err
			}
			row := make(map[string]any, len(descs))
			for i, d := range descs {
				row[d.Name] = values[i]
			}
			out = append(out, row)
		}
		return out, rows.Err()
	}
}
