// Package metrics derives control-plane metrics from ForgeX run artifacts.
//
// It quantifies the value of the control plane beyond a single success/fail
// verdict: how often policy denied a tool call, how many contract validations
// failed, how many artifacts were missing, and so on. Metrics are computed from
// the append-only streams already persisted per run, so no new run-time state is
// required and missing optional streams are simply treated as zero.
package metrics
