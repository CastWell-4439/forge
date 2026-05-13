// scripts/lint_structure.go
//
// 架构不变量 lint 工具。检查：
// 1. agent 内部依赖方向（core ← structured ← planning ← session ← harness ← workers）
// 2. core/ 零依赖（不 import internal/ 子包）
// 3. 文件大小限制（>300 行 warning，>500 行 error）
// 4. handler 文件命名约定（*_handler.go）
//
// 用法：go run scripts/lint_structure.go
//
//go:build ignore

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// agent 子包的依赖层级（数字越小层级越低，低层不能 import 高层）
var agentPackageLevel = map[string]int{
	"core":       0,
	"structured": 1,
	"planning":   2,
	"session":    3,
	"harness":    4,
	"workers":    5,
	"mcp":        4, // 同层级 harness，可被 harness 使用
	"rag":        4,
	"memory":     4,
	"guardrails": 4,
	"checkpoint": 4,
}

var (
	importRe     = regexp.MustCompile(`^\s*"(.+)"`)
	agentPkgBase = "github.com/castwell/forge/internal/agent/"
)

type violation struct {
	file    string
	line    int
	message string
	level   string // "error" or "warning"
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	var violations []violation
	errors := 0
	warnings := 0

	// Walk all .go files under internal/agent/
	agentDir := filepath.Join(root, "internal", "agent")
	filepath.Walk(agentDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // 测试文件不检查
		}

		relPath, _ := filepath.Rel(root, path)
		relPath = filepath.ToSlash(relPath)

		// 确定当前文件所在的 agent 子包
		agentRel, _ := filepath.Rel(agentDir, path)
		agentRel = filepath.ToSlash(agentRel)
		parts := strings.Split(agentRel, "/")
		if len(parts) < 2 {
			return nil // agent.go 根文件，跳过
		}
		currentPkg := parts[0]
		currentLevel, known := agentPackageLevel[currentPkg]
		if !known {
			return nil
		}

		// 检查 import
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		lineCount := 0
		inImport := false

		for scanner.Scan() {
			lineNum++
			lineCount++
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)

			// 跟踪 import 块
			if trimmed == "import (" {
				inImport = true
				continue
			}
			if inImport && trimmed == ")" {
				inImport = false
				continue
			}

			// 检查单行 import 或 import 块内
			checkLine := ""
			if strings.HasPrefix(trimmed, "import \"") {
				checkLine = trimmed
			} else if inImport {
				checkLine = trimmed
			}

			if checkLine == "" {
				continue
			}

			matches := importRe.FindStringSubmatch(checkLine)
			if matches == nil {
				continue
			}
			importPath := matches[1]

			// 规则 1：core 不能 import internal/ 子包
			if currentPkg == "core" && strings.Contains(importPath, "forge/internal/") {
				violations = append(violations, violation{
					file:    relPath,
					line:    lineNum,
					message: fmt.Sprintf("core/ 禁止 import internal/ 子包，但 import 了 %q", importPath),
					level:   "error",
				})
			}

			// 规则 2：依赖方向检查
			if strings.HasPrefix(importPath, agentPkgBase) {
				targetPkg := strings.TrimPrefix(importPath, agentPkgBase)
				// 处理子路径（如 mcp/jsonrpc → mcp）
				if idx := strings.Index(targetPkg, "/"); idx >= 0 {
					targetPkg = targetPkg[:idx]
				}
				targetLevel, targetKnown := agentPackageLevel[targetPkg]
				if targetKnown && targetLevel > currentLevel {
					violations = append(violations, violation{
						file:    relPath,
						line:    lineNum,
						message: fmt.Sprintf("依赖方向违反：%s (level %d) import %s (level %d)，低层不能依赖高层", currentPkg, currentLevel, targetPkg, targetLevel),
						level:   "error",
					})
				}
			}
		}

		// 规则 3：文件大小检查
		if lineCount > 500 {
			violations = append(violations, violation{
				file:    relPath,
				line:    0,
				message: fmt.Sprintf("文件 %d 行，超过 500 行上限，应拆分", lineCount),
				level:   "error",
			})
		} else if lineCount > 300 {
			violations = append(violations, violation{
				file:    relPath,
				line:    0,
				message: fmt.Sprintf("文件 %d 行，超过 300 行建议值，考虑拆分", lineCount),
				level:   "warning",
			})
		}

		// 规则 4：workers/ 下的 handler 文件命名约定
		if currentPkg == "workers" && !strings.HasSuffix(filepath.Base(path), "_handler.go") {
			base := filepath.Base(path)
			// 允许的非 handler 文件
			allowed := map[string]bool{
				"registry.go":     true,
				"handler.go":      true,
				"register_all.go": true,
				"config.go":       true,
			}
			if !allowed[base] {
				violations = append(violations, violation{
					file:    relPath,
					line:    0,
					message: fmt.Sprintf("workers/ 下的工具文件应以 _handler.go 结尾，当前文件名 %s", base),
					level:   "warning",
				})
			}
		}

		return nil
	})

	// 输出结果
	for _, v := range violations {
		prefix := "⚠️  WARNING"
		if v.level == "error" {
			prefix = "❌ ERROR"
			errors++
		} else {
			warnings++
		}
		if v.line > 0 {
			fmt.Printf("%s %s:%d — %s\n", prefix, v.file, v.line, v.message)
		} else {
			fmt.Printf("%s %s — %s\n", prefix, v.file, v.message)
		}
	}

	fmt.Printf("\n--- 结构 lint 结果：%d error(s), %d warning(s) ---\n", errors, warnings)

	if errors > 0 {
		os.Exit(1)
	}
}
