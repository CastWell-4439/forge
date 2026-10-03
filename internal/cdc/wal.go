// Package cdc — PGWALSource implements CDC via PostgreSQL logical replication (WAL streaming).
package cdc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// PGWALSource implements the Source interface using PostgreSQL WAL logical replication.
// It connects via the replication protocol and streams changes in real time.
//
// Requirements:
//   - wal_level = logical in postgresql.conf
//   - A replication slot (created automatically if AutoCreateSlot is true)
//   - A publication covering the target table(s)
//
// Connection string must include "replication=database", e.g.:
//
//	postgres://user:pass@localhost:5432/mydb?replication=database
type PGWALSource struct {
	connStr        string
	config         SourceConfig
	slotName       string
	publication    string
	autoCreateSlot bool
	closed         chan struct{}

	// standbyInterval controls how often we send standby status updates
	// to the server (prevents replication timeout).
	standbyInterval time.Duration

	// confirmedLSN tracks the last LSN we've successfully processed.
	confirmedLSN pglogrepl.LSN

	// relationMap caches relation metadata from the replication stream.
	// Key: relation ID, Value: RelationMessage.
	relationMap map[uint32]*pglogrepl.RelationMessageV2
}

// PGWALOption configures a PGWALSource.
type PGWALOption func(*PGWALSource)

// WithAutoCreateSlot enables automatic creation of the replication slot on first connect.
func WithAutoCreateSlot(auto bool) PGWALOption {
	return func(s *PGWALSource) {
		s.autoCreateSlot = auto
	}
}

// WithStandbyInterval sets the standby status update interval.
func WithStandbyInterval(d time.Duration) PGWALOption {
	return func(s *PGWALSource) {
		s.standbyInterval = d
	}
}

// NewPGWALSource creates a new WAL-based CDC source.
// connStr must include "replication=database".
// publication is the PG publication name (e.g. "forge_pub").
func NewPGWALSource(connStr string, config SourceConfig, publication string, opts ...PGWALOption) *PGWALSource {
	slotName := config.SlotName
	if slotName == "" {
		slotName = "forge_cdc"
	}

	s := &PGWALSource{
		connStr:         connStr,
		config:          config,
		slotName:        slotName,
		publication:     publication,
		autoCreateSlot:  true,
		closed:          make(chan struct{}),
		standbyInterval: 10 * time.Second,
		relationMap:     make(map[uint32]*pglogrepl.RelationMessageV2),
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

// Subscribe connects to PG and streams WAL changes.
// It blocks until ctx is cancelled, Close() is called, or an unrecoverable error occurs.
func (s *PGWALSource) Subscribe(ctx context.Context, handler func(Event)) error {
	conn, err := pgconn.Connect(ctx, s.connStr)
	if err != nil {
		return fmt.Errorf("pg-wal: connect: %w", err)
	}
	defer conn.Close(ctx)

	// Ensure replication slot exists.
	if s.autoCreateSlot {
		if err := s.ensureSlot(ctx, conn); err != nil {
			return err
		}
	}

	// Ensure the publication exists and covers the target table. README
	// promises "自动创建 Replication Slot + Publication"; without this the
	// replication start fails against a fresh database.
	if err := s.ensurePublication(ctx, conn); err != nil {
		return err
	}

	// Identify system to get the current WAL position.
	sysident, err := pglogrepl.IdentifySystem(ctx, conn)
	if err != nil {
		return fmt.Errorf("pg-wal: identify system: %w", err)
	}

	startLSN := sysident.XLogPos
	if s.confirmedLSN > 0 {
		startLSN = s.confirmedLSN
	}

	// Start replication with pgoutput plugin.
	err = pglogrepl.StartReplication(ctx, conn, s.slotName, startLSN,
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '2'",
				fmt.Sprintf("publication_names '%s'", s.publication),
			},
		},
	)
	if err != nil {
		return fmt.Errorf("pg-wal: start replication: %w", err)
	}

	log.Printf("INFO: pg-wal: streaming from LSN %s, slot=%s, pub=%s",
		startLSN, s.slotName, s.publication)

	standbyTicker := time.NewTicker(s.standbyInterval)
	defer standbyTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return nil
		case <-standbyTicker.C:
			// Send standby status to keep replication alive.
			err := pglogrepl.SendStandbyStatusUpdate(ctx, conn,
				pglogrepl.StandbyStatusUpdate{WALWritePosition: s.confirmedLSN})
			if err != nil {
				log.Printf("WARN: pg-wal: standby status update: %v", err)
			}
		default:
			// Receive next message with a short deadline to allow select polling.
			recvCtx, cancel := context.WithDeadline(ctx, time.Now().Add(1*time.Second))
			rawMsg, err := conn.ReceiveMessage(recvCtx)
			cancel()

			if err != nil {
				if pgconn.Timeout(err) {
					continue // receive timeout, loop back to check closed/ctx
				}
				return fmt.Errorf("pg-wal: receive: %w", err)
			}

			if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
				return fmt.Errorf("pg-wal: server error: %s (code %s)", errMsg.Message, errMsg.Code)
			}

			copyData, ok := rawMsg.(*pgproto3.CopyData)
			if !ok {
				continue
			}

			switch copyData.Data[0] {
			case pglogrepl.XLogDataByteID:
				xld, err := pglogrepl.ParseXLogData(copyData.Data[1:])
				if err != nil {
					log.Printf("WARN: pg-wal: parse xlog: %v", err)
					continue
				}

				events, err := s.processWALData(xld)
				if err != nil {
					log.Printf("WARN: pg-wal: process wal: %v", err)
					continue
				}

				for _, event := range events {
					if !s.config.MatchesEvent(event) {
						continue
					}
					if !s.matchesFilter(event) {
						continue
					}
					handler(event)
				}

				// Advance confirmed LSN.
				if xld.WALStart+pglogrepl.LSN(len(xld.WALData)) > s.confirmedLSN {
					s.confirmedLSN = xld.WALStart + pglogrepl.LSN(len(xld.WALData))
				}

			case pglogrepl.PrimaryKeepaliveMessageByteID:
				pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(copyData.Data[1:])
				if err != nil {
					log.Printf("WARN: pg-wal: parse keepalive: %v", err)
					continue
				}
				if pkm.ReplyRequested {
					err := pglogrepl.SendStandbyStatusUpdate(ctx, conn,
						pglogrepl.StandbyStatusUpdate{WALWritePosition: s.confirmedLSN})
					if err != nil {
						log.Printf("WARN: pg-wal: keepalive reply: %v", err)
					}
				}
			}
		}
	}
}

// Close stops the WAL source.
func (s *PGWALSource) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// ensureSlot creates the replication slot if it doesn't exist.
func (s *PGWALSource) ensureSlot(ctx context.Context, conn *pgconn.PgConn) error {
	_, err := pglogrepl.CreateReplicationSlot(ctx, conn, s.slotName, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{
			Mode: pglogrepl.LogicalReplication,
		},
	)
	if err != nil {
		// Slot already exists is not an error.
		if strings.Contains(err.Error(), "already exists") {
			log.Printf("INFO: pg-wal: replication slot %q already exists", s.slotName)
			return nil
		}
		return fmt.Errorf("pg-wal: create slot %q: %w", s.slotName, err)
	}
	log.Printf("INFO: pg-wal: created replication slot %q", s.slotName)
	return nil
}

// ensurePublication makes the configured publication cover the target table:
// it creates the publication when missing and adds the table when the
// publication exists without it. DDL and catalog lookups on this raw
// connection take no bind parameters, so both names are validated as
// identifiers first — anything unusual is refused rather than interpolated.
func (s *PGWALSource) ensurePublication(ctx context.Context, conn *pgconn.PgConn) error {
	if err := validatePubIdentifier(s.publication); err != nil {
		return fmt.Errorf("pg-wal: publication: %w", err)
	}
	schema, table, err := splitRelation(s.config.Table)
	if err != nil {
		return fmt.Errorf("pg-wal: table: %w", err)
	}

	// Already published?
	tableFilter := fmt.Sprintf("tablename = '%s'", table)
	if schema != "" {
		tableFilter += fmt.Sprintf(" AND schemaname = '%s'", schema)
	}
	member, err := queryHasRows(ctx, conn, fmt.Sprintf(
		"SELECT 1 FROM pg_publication_tables WHERE pubname = '%s' AND %s",
		s.publication, tableFilter))
	if err != nil {
		return fmt.Errorf("pg-wal: check publication: %w", err)
	}
	if member {
		return nil
	}

	rel := quotePubIdent(s.config.Table)
	pubExists, err := queryHasRows(ctx, conn, fmt.Sprintf(
		"SELECT 1 FROM pg_publication WHERE pubname = '%s'", s.publication))
	if err != nil {
		return fmt.Errorf("pg-wal: check publication name: %w", err)
	}

	if !pubExists {
		ddl := fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s", quotePubIdent(s.publication), rel)
		if err := execDDL(ctx, conn, ddl); err != nil {
			return fmt.Errorf("pg-wal: create publication: %w", err)
		}
		log.Printf("INFO: pg-wal: created publication %q for %s", s.publication, s.config.Table)
		return nil
	}

	ddl := fmt.Sprintf("ALTER PUBLICATION %s ADD TABLE %s", quotePubIdent(s.publication), rel)
	if err := execDDL(ctx, conn, ddl); err != nil {
		return fmt.Errorf("pg-wal: add table to publication: %w", err)
	}
	log.Printf("INFO: pg-wal: publication %q now covers %s", s.publication, s.config.Table)
	return nil
}

// queryHasRows runs a validated single-statement SELECT on the raw
// replication connection and reports whether it returned any row. The SQL
// must already be injection-safe (validated identifiers, quoted literals).
//
// It goes through conn.Exec (simple query protocol): replication
// connections refuse the extended query protocol, so ExecParams/parameter
// binding cannot be used here at all (CI caught this as SQLSTATE 08P01).
func queryHasRows(ctx context.Context, conn *pgconn.PgConn, sql string) (bool, error) {
	results, err := conn.Exec(ctx, sql).ReadAll()
	if err != nil {
		return false, err
	}
	for _, res := range results {
		if len(res.Rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// execDDL runs a validated DDL statement, draining the result so the
// connection stays usable for the next command.
func execDDL(ctx context.Context, conn *pgconn.PgConn, sql string) error {
	return conn.Exec(ctx, sql).Close()
}

// validatePubIdentifier accepts the identifier shape PostgreSQL publications
// realistically use: letters, digits, underscore.
func validatePubIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return fmt.Errorf("invalid identifier %q (letters, digits and underscore only)", name)
		}
	}
	return nil
}

// splitRelation splits "schema.table" into its parts; "table" alone returns
// an empty schema.
func splitRelation(rel string) (schema, table string, err error) {
	parts := strings.Split(rel, ".")
	if len(parts) > 2 || parts[0] == "" {
		return "", "", fmt.Errorf("invalid relation %q", rel)
	}
	for _, p := range parts {
		if err := validatePubIdentifier(p); err != nil {
			return "", "", fmt.Errorf("invalid relation %q: %w", rel, err)
		}
	}
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", parts[0], nil
}

// quotePubIdent quotes a validated relation for DDL use.
func quotePubIdent(rel string) string {
	parts := strings.Split(rel, ".")
	for i, p := range parts {
		parts[i] = `"` + p + `"`
	}
	return strings.Join(parts, ".")
}

// processWALData parses pgoutput v2 messages from a WAL data chunk.
func (s *PGWALSource) processWALData(xld pglogrepl.XLogData) ([]Event, error) {
	msg, err := pglogrepl.ParseV2(xld.WALData, false)
	if err != nil {
		return nil, fmt.Errorf("parse pgoutput: %w", err)
	}

	switch m := msg.(type) {
	case *pglogrepl.RelationMessageV2:
		// Cache relation metadata for later column lookups.
		s.relationMap[m.RelationID] = m
		return nil, nil

	case *pglogrepl.InsertMessageV2:
		rel, ok := s.relationMap[m.RelationID]
		if !ok {
			return nil, fmt.Errorf("unknown relation %d", m.RelationID)
		}
		newData := tupleToMap(rel, m.Tuple)
		return []Event{{
			Table:     rel.RelationName,
			Operation: OpInsert,
			NewData:   newData,
			Timestamp: time.Now(),
			LSN:       uint64(xld.WALStart),
			Raw:       mustJSON(newData),
		}}, nil

	case *pglogrepl.UpdateMessageV2:
		rel, ok := s.relationMap[m.RelationID]
		if !ok {
			return nil, fmt.Errorf("unknown relation %d", m.RelationID)
		}
		var oldData map[string]interface{}
		if m.OldTuple != nil {
			oldData = tupleToMap(rel, m.OldTuple)
		}
		newData := tupleToMap(rel, m.NewTuple)
		return []Event{{
			Table:     rel.RelationName,
			Operation: OpUpdate,
			OldData:   oldData,
			NewData:   newData,
			Timestamp: time.Now(),
			LSN:       uint64(xld.WALStart),
			Raw:       mustJSON(newData),
		}}, nil

	case *pglogrepl.DeleteMessageV2:
		rel, ok := s.relationMap[m.RelationID]
		if !ok {
			return nil, fmt.Errorf("unknown relation %d", m.RelationID)
		}
		var oldData map[string]interface{}
		if m.OldTuple != nil {
			oldData = tupleToMap(rel, m.OldTuple)
		}
		return []Event{{
			Table:     rel.RelationName,
			Operation: OpDelete,
			OldData:   oldData,
			Timestamp: time.Now(),
			LSN:       uint64(xld.WALStart),
			Raw:       mustJSON(oldData),
		}}, nil

	case *pglogrepl.BeginMessage, *pglogrepl.CommitMessage:
		// Transaction boundaries — no events to emit.
		return nil, nil

	case *pglogrepl.TruncateMessageV2:
		// Truncate is not modeled as INSERT/UPDATE/DELETE; skip.
		return nil, nil

	default:
		// TypeMessage, OriginMessage, StreamStartMessage, etc. — skip.
		return nil, nil
	}
}

// tupleToMap converts a pgoutput TupleData to a string-keyed map
// using the cached relation's column names.
func tupleToMap(rel *pglogrepl.RelationMessageV2, tuple *pglogrepl.TupleData) map[string]interface{} {
	if tuple == nil {
		return nil
	}
	data := make(map[string]interface{}, len(tuple.Columns))
	for i, col := range tuple.Columns {
		if i >= len(rel.Columns) {
			break
		}
		colName := rel.Columns[i].Name
		switch col.DataType {
		case 'n': // null
			data[colName] = nil
		case 'u': // unchanged toast
			data[colName] = "(unchanged)"
		case 't': // text
			data[colName] = string(col.Data)
		}
	}
	return data
}

// matchesFilter delegates to the shared filter evaluator in postgres.go.
func (s *PGWALSource) matchesFilter(event Event) bool {
	if s.config.Filter == "" {
		return true
	}
	return evaluateSimpleFilter(s.config.Filter, event.NewData)
}

// mustJSON marshals v to JSON, returning nil on error.
func mustJSON(v interface{}) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
