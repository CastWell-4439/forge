package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentcore "github.com/castwell/forge/internal/agent/core"
	forgexmodel "github.com/castwell/forge/internal/forgex/model"
)

// Evidence projection: reading recorded tool calls for verification.
//
// This is the bridge between the planes' data and the agent core's judgement.
// The core decides what a disagreement IS (core/verify.go); this file knows how
// a recorded call is SHAPED, which is control-plane knowledge, and projects it
// into the shape the judge reads.
//
// It reads tool_calls.jsonl directly rather than through the SQLite index, for
// the same reason eval and skillpack distillation do: the file is the record,
// the index is a query accelerator, and verification wants to read a handful of
// recent runs rather than query across all of them. Adding an index table here
// would be infrastructure without a consumer.

// evidenceCallFiles is the per-run file verification reads.
const toolCallsFileName = "tool_calls.jsonl"

// LoadEvidence reads tool calls from the newest runs under a root.
//
// The runs are ordered by directory modification time rather than by parsing
// every run's metadata: verification asks what the world looks like NOW, so the
// newest directories are the ones that matter, and a run whose metadata is
// unreadable should not stop the scan.
func LoadEvidence(root string, maxRuns, maxCalls int) ([]agentcore.EvidenceToolCall, error) {
	if maxRuns <= 0 {
		maxRuns = 20
	}
	if maxCalls <= 0 {
		maxCalls = 500
	}

	runsDir := filepath.Join(root, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No runs yet is not a failure: verification reports "no evidence".
			return nil, nil
		}
		return nil, fmt.Errorf("read runs dir %s: %w", runsDir, err)
	}

	type runDir struct {
		path string
		mod  time.Time
	}
	var dirs []runDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, runDir{path: filepath.Join(runsDir, e.Name()), mod: info.ModTime()})
	}
	// Newest first: old evidence is worse than none when the question is about
	// the current world.
	sort.SliceStable(dirs, func(i, j int) bool { return dirs[i].mod.After(dirs[j].mod) })
	if len(dirs) > maxRuns {
		dirs = dirs[:maxRuns]
	}

	var out []agentcore.EvidenceToolCall
	for _, dir := range dirs {
		if len(out) >= maxCalls {
			break
		}
		calls, err := loadToolCalls(filepath.Join(dir.path, toolCallsFileName))
		if err != nil {
			// One unreadable run must not sink the scan: verification is a
			// review aid, and a partial sample still says something.
			continue
		}
		for _, call := range calls {
			out = append(out, projectToolCall(call))
			if len(out) >= maxCalls {
				break
			}
		}
	}
	return out, nil
}

// loadToolCalls reads one run's tool calls, tolerating an absent file.
func loadToolCalls(path string) ([]forgexmodel.ToolCall, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []forgexmodel.ToolCall
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var call forgexmodel.ToolCall
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			// A malformed line is skipped rather than failing the file: the
			// rest of the evidence is still usable.
			continue
		}
		out = append(out, call)
	}
	return out, nil
}

// projectToolCall turns a recorded call into the judge's evidence shape.
//
// Paths come from the call's arguments and results. There is no schema to rely
// on — Args and Result are free-form maps filled by arbitrary workers — so the
// extraction walks their values and keeps whatever has a path shape. That is a
// deliberate limitation: a worker that puts a path under an unusual key still
// has it found, while a value that merely contains a dot is not mistaken for a
// file.
func projectToolCall(call forgexmodel.ToolCall) agentcore.EvidenceToolCall {
	out := agentcore.EvidenceToolCall{
		RunID:    call.RunID,
		ToolName: call.ToolName,
		Failed:   call.Error != "",
		Error:    call.Error,
		At:       call.EndedAt,
	}
	if out.At.IsZero() {
		out.At = call.StartedAt
	}

	out.Paths = append(out.Paths, pathsIn(call.Args)...)
	out.Paths = append(out.Paths, pathsIn(call.Result)...)
	out.Paths = dedupeStrings(out.Paths)

	var text strings.Builder
	writeValues(&text, call.Args)
	writeValues(&text, call.Result)
	out.Text = text.String()

	return out
}

// pathsIn collects path-shaped strings from a free-form value.
func pathsIn(value any) []string {
	var out []string
	walkValues(value, func(s string) {
		if looksLikePath(s) {
			out = append(out, s)
		}
	})
	return out
}

// writeValues flattens a free-form value into text for value searching.
func writeValues(b *strings.Builder, value any) {
	walkValues(value, func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
	})
}

// walkValues calls fn for every string found in a nested value.
//
// It descends through maps and slices because a worker's output shape is its
// own business; restricting the walk to a known key would silently stop finding
// evidence the day a worker renames a field.
func walkValues(value any, fn func(string)) {
	switch v := value.(type) {
	case nil:
		return
	case string:
		fn(v)
	case []any:
		for _, item := range v {
			walkValues(item, fn)
		}
	case map[string]any:
		// Sorted keys so the flattened text is deterministic: the same call must
		// project to the same evidence, or two reviews would differ.
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkValues(v[k], fn)
		}
	default:
		fn(fmt.Sprintf("%v", v))
	}
}

// looksLikePath reports whether a string is a file path worth indexing.
//
// The test is a known source/config extension on the last segment, the same one
// the assertion extractor uses. A bare directory is not included: "internal/"
// says little, while "internal/agent/core/tools.go" names a file whose fate can
// be checked.
func looksLikePath(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "\n\r\t") {
		return false
	}
	idx := strings.LastIndexByte(s, '.')
	if idx <= 0 || idx == len(s)-1 {
		return false
	}
	_, ok := agentcore.PathExtensionKnown(strings.ToLower(s[idx+1:]))
	return ok
}

// dedupeStrings removes duplicates while preserving order.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
