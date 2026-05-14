//go:build ignore

// lint_structure.go — Forge 架构 lint 脚本
// 检查项：
//   1. agent 内部依赖方向（core←structured←planning←session←harness←workers）
//   2. core 包零依赖（不能 import 任何 internal/ 子包）
//   3. 文件大小（>500 行 = error，>300 行 = warning）
//   4. workers handler 命名（必须以 _handler.go 结尾）
//
// 用法：go run scripts/lint_structure.go

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// agent 子包的层级顺序（数字小的不能 import 数字大的）
var agentLayerOrder = map[string]int{
	"core":       0,
	"structured": 1,
	"planning":   2,
	"session":    3,
	"harness":    4,
	"workers":    5,
}

// 独立模块，层级与 harness 同级 (4)，不能 import workers
var agentSideModules = map[string]int{
	"mcp":        4,
	"rag":        4,
	"memory":     4,
	"guardrails": 4,
	"checkpoint": 4,
}

var importRe = regexp.MustCompile(`^\s*"(.+)"`)

type violation struct {
	file    string
	line    int
	level   string // "ERROR" or "WARN"
	message string
}

func main() {
	root := findProjectRoot()
	agentDir := filepath.Join(root, "internal", "agent")

	var violations []violation

	// Walk all .go files (non-test)
	err := filepath.Walk(agentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		// Determine which agent sub-package this file belongs to
		subPkg := getAgentSubPackage(rel)

		// Check 1 & 2: dependency direction
		imports := extractImports(path)
		for importLine, importPath := range imports {
			vs := checkDependency(rel, subPkg, importPath, importLine)
			violations = append(violations, vs...)
		}

		// Check 3: file size
		lineCount := countLines(path)
		if lineCount > 500 {
			violations = append(violations, violation{
				file:    rel,
				level:   "ERROR",
				message: fmt.Sprintf("file has %d lines (max 500)", lineCount),
			})
		} else if lineCount > 300 {
			violations = append(violations, violation{
				file:    rel,
				level:   "WARN",
				message: fmt.Sprintf("file has %d lines (recommended max 300)", lineCount),
			})
		}

		// Check 4: workers handler naming
		if subPkg == "workers" && !strings.HasSuffix(filepath.Base(path), "_handler.go") {
			base := filepath.Base(path)
			// 允许 registry.go, config.go, register_all.go, handler.go 等基础设施文件
			allowed := map[string]bool{
				"registry.go":     true,
				"config.go":       true,
				"register_all.go": true,
				"handler.go":      true,
			}
			if !allowed[base] {
				violations = append(violations, violation{
					file:    rel,
					level:   "WARN",
					message: fmt.Sprintf("workers file should end with _handler.go (got %s)", base),
				})
			}
		}

		return nil
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "error walking directory: %v\n", err)
		os.Exit(2)
	}

	// Report
	hasError := false
	for _, v := range violations {
		prefix := "⚠️  WARN"
		if v.level == "ERROR" {
			prefix = "❌ ERROR"
			hasError = true
		}
		loc := v.file
		if v.line > 0 {
			loc = fmt.Sprintf("%s:%d", v.file, v.line)
		}
		fmt.Printf("%s  %s  %s\n", prefix, loc, v.message)
	}

	if hasError {
		fmt.Printf("\n❌ Lint FAILED (%d errors, %d warnings)\n", countLevel(violations, "ERROR"), countLevel(violations, "WARN"))
		os.Exit(1)
	}

	warnCount := countLevel(violations, "WARN")
	if warnCount > 0 {
		fmt.Printf("\n✅ Lint PASSED with %d warnings\n", warnCount)
	} else {
		fmt.Println("\n✅ Lint PASSED — all checks clean")
	}
}

func getAgentSubPackage(relPath string) string {
	// relPath like "internal/agent/core/types.go" or "internal/agent/agent.go"
	parts := strings.Split(relPath, "/")
	if len(parts) >= 4 && parts[0] == "internal" && parts[1] == "agent" {
		return parts[2] // "core", "harness", etc.
	}
	return "" // root agent.go
}

func checkDependency(file, subPkg, importPath string, line int) []violation {
	var vs []violation

	// Only check imports within forge internal/agent/
	if !strings.Contains(importPath, "internal/agent/") {
		// Check 2: core must not import any internal/ package
		if subPkg == "core" && strings.Contains(importPath, "forge") && strings.Contains(importPath, "internal/") {
			vs = append(vs, violation{
				file:    file,
				line:    line,
				level:   "ERROR",
				message: fmt.Sprintf("core/ must not import internal/ packages (imports %s)", importPath),
			})
		}
		return vs
	}

	// Extract target sub-package from import path
	targetPkg := extractTargetSubPkg(importPath)
	if targetPkg == "" {
		return vs
	}

	// Get layer numbers
	srcLayer, srcOk := getLayer(subPkg)
	tgtLayer, tgtOk := getLayer(targetPkg)

	if !srcOk || !tgtOk {
		return vs // Unknown packages, skip
	}

	// Rule: lower layer cannot import higher layer
	if srcLayer < tgtLayer {
		vs = append(vs, violation{
			file:    file,
			line:    line,
			level:   "ERROR",
			message: fmt.Sprintf("dependency violation: %s (layer %d) imports %s (layer %d)", subPkg, srcLayer, targetPkg, tgtLayer),
		})
	}

	return vs
}

func getLayer(pkg string) (int, bool) {
	if l, ok := agentLayerOrder[pkg]; ok {
		return l, true
	}
	if l, ok := agentSideModules[pkg]; ok {
		return l, true
	}
	return 0, false
}

func extractTargetSubPkg(importPath string) string {
	// importPath like "forge/internal/agent/core" or "forge/internal/agent/harness"
	idx := strings.Index(importPath, "internal/agent/")
	if idx == -1 {
		return ""
	}
	rest := importPath[idx+len("internal/agent/"):]
	parts := strings.SplitN(rest, "/", 2)
	return parts[0]
}

func extractImports(path string) map[int]string {
	result := make(map[int]string)
	f, err := os.Open(path)
	if err != nil {
		return result
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	inImport := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == "import (" {
			inImport = true
			continue
		}
		if inImport && trimmed == ")" {
			inImport = false
			continue
		}
		if inImport {
			matches := importRe.FindStringSubmatch(trimmed)
			if len(matches) >= 2 {
				result[lineNum] = matches[1]
			}
		}
		// Single-line import
		if strings.HasPrefix(trimmed, "import \"") {
			imp := strings.TrimPrefix(trimmed, "import \"")
			imp = strings.TrimSuffix(imp, "\"")
			result[lineNum] = imp
		}
	}
	return result
}

func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		count++
	}
	return count
}

func countLevel(vs []violation, level string) int {
	n := 0
	for _, v := range vs {
		if v.level == level {
			n++
		}
	}
	return n
}

func findProjectRoot() string {
	// Try to find go.mod by walking up from cwd
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fallback: assume D:\forge
	return `D:\forge`
}
