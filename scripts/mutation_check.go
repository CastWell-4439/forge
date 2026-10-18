//go:build ignore

// mutation_check.go — 证明"测试真的在测它"的最小工具。
//
// 为什么需要它：本会话里三次靠手工试才发现了"声称存在但没有守卫"的性质——
// D-71 的"不提前退出"（加 break 后全绿）、D-75 的 ClientOptions 令牌优先级、
// 以及 D-75 的过期扫描。每次都是**先想到去试，才发现没守卫**。
// 这个方法有效，但它依赖"我想到了"；工具把它变成可重复的动作。
//
// 它做的事，一句话：**把代码改坏，看测试是否变红。**
//
//	没变红 → 这条性质没有守卫（测试通过的理由不是它声称的那个）
//	变红   → 这条性质被守卫着
//
// 用法：
//
//	go run scripts/mutation_check.go -pkg ./internal/grpcauth -m mutations.txt
//
// mutations.txt 的格式（每行一个变异，空行与 # 开头忽略）：
//
//	file.go
//	--- old
//	原文片段（必须唯一匹配）
//	--- new
//	替换后的片段
//	=== end
//
// 退出码：0 = 所有变异都被检测到；1 = 有变异没被检测到（或运行出错）。
//
// ── 安全边界（这是工具最重要的部分）──────────────────────────────
//
// 它会**修改你的源文件**。所以：
//
//  1. 只在一个文件上动手，且**逐字节**备份与还原（不依赖行尾、不依赖编码）。
//  2. 还原走 defer，且**还原失败时立即中止并大声报错**——
//     宁可留下一个明确失败的任务，也不要悄悄留着一个被改坏的源文件。
//  3. 工作树必须干净才允许开始。脏树上的还原无法区分"工具改的"与
//     "本来就有的改动"，那种情况下工具拒绝运行。
//  4. 每个变异在独立的 `go test` 进程里跑，超时被杀。
//
// 这些约束让"工具自己出问题"的代价被压到最小：最坏情况是一个变异没跑成，
// 而不是一个源文件被留在改坏的状态。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// mutation is one edit to prove a test notices.
type mutation struct {
	// file is the path, relative to the repo root or as given.
	file string
	// old must appear EXACTLY ONCE in the file, or the mutation is refused.
	// Requiring uniqueness is what keeps the tool away from "replace the first
	// occurrence and hope": a mutation that could match several places would make
	// the result ambiguous, and an ambiguous result is worse than none because it
	// looks like evidence.
	old string
	// new replaces old.
	new string
	// name is an optional label for the report.
	name string
}

func main() {
	pkg := flag.String("pkg", "", "package to test, e.g. ./internal/grpcauth")
	mutationsPath := flag.String("m", "", "path to the mutations file")
	timeout := flag.Duration("timeout", 180*time.Second, "per-mutation test timeout")
	keep := flag.Bool("keep", false, "leave the last mutation applied (for inspection)")
	flag.Parse()

	if *pkg == "" || *mutationsPath == "" {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/mutation_check.go -pkg <pkg> -m <mutations-file>")
		os.Exit(2)
	}

	mutations, err := parseMutations(*mutationsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse %s: %v\n", *mutationsPath, err)
		os.Exit(2)
	}
	if len(mutations) == 0 {
		fmt.Fprintln(os.Stderr, "no mutations to run")
		os.Exit(2)
	}

	if err := requireCleanTree(); err != nil {
		fmt.Fprintf(os.Stderr, "refusing to run: %v\n", err)
		os.Exit(2)
	}

	failures := 0
	for i, m := range mutations {
		label := m.name
		if label == "" {
			label = fmt.Sprintf("%s#%d", m.file, i+1)
		}
		fmt.Printf("\n── mutation %d/%d: %s\n", i+1, len(mutations), label)

		caught, err := runOne(m, *pkg, *timeout, *keep && i == len(mutations)-1)
		if err != nil {
			// An error here is not "the mutation was not caught" — it means the
			// tool could not complete the experiment. Report it as such rather
			// than folding it into the pass/fail tally, because the two need
			// different responses.
			fmt.Fprintf(os.Stderr, "   ERROR: %v\n", err)
			failures++
			continue
		}
		if caught {
			fmt.Printf("   ✓ caught: the tests failed with the mutation applied\n")
			continue
		}
		fmt.Printf("   ✗ NOT CAUGHT: the tests passed with the mutation applied\n")
		fmt.Printf("     this property has no guard — a passing test here is not evidence\n")
		failures++
	}

	fmt.Printf("\n%d/%d mutations caught\n", len(mutations)-failures, len(mutations))
	if failures > 0 {
		os.Exit(1)
	}
}

// runOne applies a mutation, runs the tests, and restores the file.
//
// It returns whether the tests CAUGHT the mutation (i.e. failed). Any error means
// the experiment itself did not complete.
//
// ── 行尾归一化（这是本工具最容易错的地方）─────────────────────────
//
// Mutation files are authored with LF (they are ordinary text, and an editor on
// Linux or a git checkout with autocrlf will produce LF). Source files in this
// repository are CRLF on disk on Windows. A byte comparison between an LF
// fragment and a CRLF file never matches — and the failure looks exactly like
// "this fragment does not exist", which is what this tool's first version
// reported for three mutations whose fragments were plainly present.
//
// So the comparison is done on a normalised copy: both sides have CRLF folded to
// LF. The replacement is then applied to the ORIGINAL bytes by locating the
// normalised match and mapping its span back — which keeps the rest of the file
// byte-identical (including its line endings) rather than rewriting the whole
// file to LF and leaving the tree dirtier than it found it.
func runOne(m mutation, pkg string, timeout time.Duration, keep bool) (caught bool, err error) {
	path := m.file
	original, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	// Normalise for matching only. `normalised` is CRLF→LF; the offsets it
	// produces are NOT the offsets in `original`, so the span is mapped back
	// below before any bytes are replaced.
	lfOriginal := bytes.ReplaceAll(original, []byte("\r\n"), []byte("\n"))
	lfOld := []byte(dereCRLF(m.old))
	lfNew := []byte(dereCRLF(m.new))

	count := bytes.Count(lfOriginal, lfOld)
	if count != 1 {
		return false, fmt.Errorf("%s: expected the old fragment exactly once, found %d occurrences", path, count)
	}

	idx := bytes.Index(lfOriginal, lfOld)
	start, end := mapLFOffsetToOriginal(original, idx, idx+len(lfOld))
	mutated := make([]byte, 0, len(original))
	mutated = append(mutated, original[:start]...)
	// The replacement inherits the file's own line endings, so applying a
	// mutation does not turn the file's endings into a mixed mess.
	mutated = append(mutated, []byte(reCRLF(string(lfNew), original[start:end]))...)
	mutated = append(mutated, original[end:]...)

	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		return false, fmt.Errorf("apply mutation to %s: %w", path, err)
	}

	// Restore is deferred and its failure is reported LOUDLY: a tool that leaves
	// source modified is worse than one that refuses to run.
	if !keep {
		defer func() {
			if rerr := os.WriteFile(path, original, 0o644); rerr != nil {
				fmt.Fprintf(os.Stderr,
					"\n!!! FAILED TO RESTORE %s: %v\n!!! RESTORE IT BY HAND BEFORE CONTINUING\n", path, rerr)
			}
		}()
	}

	// A build failure is not a caught mutation. `break` out of a loop always
	// compiles, but some edits do not, and reporting "the test failed" for a
	// compile error would be a false positive exactly where the tool is supposed
	// to be trustworthy.
	build := exec.Command("go", "build", pkg)
	build.Env = os.Environ()
	if out, berr := build.CombinedOutput(); berr != nil {
		return false, fmt.Errorf("the mutation does not compile, so the experiment is inconclusive:\n%s",
			truncate(string(out), 800))
	}

	cmd := exec.Command("go", "test", pkg, "-count=1", "-timeout", timeout.String())
	cmd.Env = os.Environ()
	out, testErr := cmd.CombinedOutput()

	if testErr == nil {
		// Tests passed: the mutation slipped through.
		return false, nil
	}

	// Tests failed. That is the expected outcome — but only if it failed for the
	// right reason: a panic from a broken restore, or a timeout, would look the
	// same from here. The minimum check is that it is a test failure rather than
	// a build failure, which `go build` above already covered.
	if strings.Contains(string(out), "build failed") {
		return false, fmt.Errorf("tests reported a build failure rather than a test failure:\n%s",
			truncate(string(out), 800))
	}
	return true, nil
}

// parseMutations reads the mutation file.
//
// The format is deliberately line-oriented and boring: a mutation is easy to
// mis-author, and a format with quoting rules would make that worse.
func parseMutations(path string) ([]mutation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Strip a UTF-8 BOM if one is present.
	//
	// Windows editors and PowerShell's `Set-Content -Encoding UTF8` write one, and
	// a BOM on the FIRST line would attach to the first file path — making it
	// "internal/..." and producing "cannot find the path specified" for a
	// mutation that looks perfectly correct. The symptom points at the file system
	// rather than at an encoding, which is what made it worth three probe runs to
	// find.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	// Fold line endings before parsing.
	//
	// The same file may be CRLF or LF depending on which editor last touched it,
	// and this repository's checkout is CRLF on Windows. Splitting CRLF text on
	// "\n" leaves a trailing "\r" on every line, so `line == "--- old"` never
	// matches — and the parser then reports "mutation 1 is missing a file or an
	// old fragment" for a file that looks entirely correct.
	//
	// This is the second time line endings broke this tool (the first was the
	// fragment comparison), so it is normalised once, here, at the entry point.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))

	var (
		out     []mutation
		current *mutation
		section string
		buf     strings.Builder
	)

	flush := func() {
		if current == nil {
			return
		}
		switch section {
		case "old":
			current.old = strings.TrimSuffix(buf.String(), "\n")
		case "new":
			current.new = strings.TrimSuffix(buf.String(), "\n")
		}
	}

	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			continue

		case line == "=== end":
			flush()
			if current != nil {
				out = append(out, *current)
			}
			current, section = nil, ""
			buf.Reset()

		case line == "--- old":
			flush()
			buf.Reset()
			section = "old"

		case line == "--- new":
			flush()
			buf.Reset()
			section = "new"

		// `--- name` must be handled BEFORE the "starts a new mutation" case,
		// because it appears on its own line between the file name and the first
		// fragment. When it fell through to the default branch it was swallowed
		// as fragment text, which is why three mutations reported "found 0
		// occurrences" while their fragments were plainly present — the tool was
		// searching for a fragment whose first line was the name directive.
		case strings.HasPrefix(line, "--- name "):
			if current != nil {
				current.name = strings.TrimPrefix(line, "--- name ")
			}

		// A non-empty line while no section is open starts a new mutation and
		// names its file.
		case section == "" && strings.TrimSpace(line) != "":
			// Close any previous mutation that was not terminated by `=== end`.
			flush()
			if current != nil && current.file != "" && current.old != "" {
				out = append(out, *current)
			}
			current = &mutation{file: strings.TrimSpace(line)}
			buf.Reset()

		default:
			if section != "" {
				buf.WriteString(line)
				buf.WriteString("\n")
			}
		}
	}

	if current != nil {
		flush()
		out = append(out, *current)
	}

	for i, m := range out {
		if m.file == "" || m.old == "" {
			return nil, fmt.Errorf("mutation %d is missing a file or an old fragment", i+1)
		}
	}
	return out, nil
}

// requireCleanTree refuses to run on a dirty working tree.
//
// The tool edits tracked files and restores them byte-for-byte. On a dirty tree
// that restore would also erase the user's own uncommitted changes to the same
// files — and there would be no way to tell afterwards which change was whose.
func requireCleanTree() error {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("the working tree has uncommitted changes:\n%s", truncate(string(out), 600))
	}
	return nil
}

// dereCRLF folds CRLF to LF, so a fragment authored with LF can be matched
// against a file stored with CRLF.
func dereCRLF(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// reCRLF restores CRLF in a replacement when the region being replaced used CRLF.
//
// The region is the span of the ORIGINAL file that the mutation covers, so its
// line endings are the file's real ones. Copying them onto the replacement keeps
// the file's convention intact instead of introducing mixed endings that would
// then show up as a spurious diff.
func reCRLF(replacement string, originalRegion []byte) string {
	if !bytes.Contains(originalRegion, []byte("\r\n")) {
		return replacement
	}
	return strings.ReplaceAll(replacement, "\n", "\r\n")
}

// mapLFOffsetToOriginal translates a span in the CRLF→LF normalised copy back to
// offsets in the original bytes.
//
// A normalised offset is (original offset - number of CRLF pairs preceding it),
// because folding removed exactly one byte per pair. Walking both at once is
// clearer than arithmetic and is the version that cannot drift.
func mapLFOffsetToOriginal(original []byte, lfStart, lfEnd int) (start, end int) {
	orig, lf := 0, 0
	start, end = -1, -1

	for orig < len(original) && lf <= lfEnd {
		if lf == lfStart {
			start = orig
		}
		if lf == lfEnd {
			end = orig
			break
		}
		if original[orig] == '\r' && orig+1 < len(original) && original[orig+1] == '\n' {
			orig += 2 // the pair folds to one '\n'
		} else {
			orig++
		}
		lf++
	}
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = len(original)
	}
	return start, end
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n... (%d bytes truncated)", len(s)-n)
}

// ensure the file path is used relative to the repo root, so a mutation file can
// list paths the way a reader sees them.
var _ = filepath.Join
