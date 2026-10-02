package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Standards section of AGENTS.md states how scripts/check.sh runs and
// which Go tests call t.Parallel. These tests read that script and the suite
// so a later edit cannot make the section false without failing here.

func TestCheckScriptMatchesTheVerificationStandard(t *testing.T) {
	t.Parallel()

	script := string(mustRead(t, filepath.Join(moduleRoot(t), "scripts", "check.sh")))

	stages := []string{"gofmt", "go vet", "golangci-lint", "go test"}
	previous := -1
	for _, stage := range stages {
		marker := `step "` + stage + `"`
		at := strings.Index(script, marker)
		if at < 0 {
			t.Errorf("scripts/check.sh does not print a %s stage", stage)
			continue
		}
		if at < previous {
			t.Errorf("stage %s is out of order in scripts/check.sh", stage)
		}
		previous = at
	}

	// gofmt -l reads the tree. The script names `gofmt -w` only as the remedy
	// it prints; running it would rewrite sources the later stages read.
	if !strings.Contains(script, "gofmt -l .") {
		t.Error("gofmt stage does not list unformatted files with gofmt -l")
	}
	if strings.Count(script, "gofmt -w") != 1 || !strings.Contains(script, `echo "fix with: gofmt -w ."`) {
		t.Error("gofmt stage rewrites sources, which are inputs of the later stages")
	}

	// A failing command is recorded and the script continues. set -e would
	// otherwise stop the run at the first red stage.
	for _, command := range []string{
		"go vet ./... || fail=1",
		"golangci-lint run --max-same-issues=0 --max-issues-per-linter=0 || fail=1",
		"go test ./... || fail=1",
	} {
		if !strings.Contains(script, command) {
			t.Errorf("scripts/check.sh is missing %q, so a failure would stop the later stages or change what they run", command)
		}
	}
	if !strings.Contains(script, `if [ "$fail" -ne 0 ]; then`) || !strings.Contains(script, "exit 1") {
		t.Error("scripts/check.sh does not exit non-zero when a stage failed")
	}
	// The unit stage has no integration build tag, so it does not need /dev/kvm.
	// The script names that command afterwards so an operator can run it, and
	// that printf is not a stage.
	if strings.Count(script, "-tags integration") != 1 || !strings.Contains(script, `printf '  go test -tags integration ./test/integration/...\n'`) {
		t.Error("scripts/check.sh runs the integration suite, which needs a KVM host")
	}
}

func TestGoTestsCallParallelUnlessTheyTouchProcessGlobalState(t *testing.T) {
	t.Parallel()

	root := moduleRoot(t)
	var problems []string
	dirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(entry.Name(), "_test.go") {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listing tests: %v", err)
	}

	for dir, testFiles := range dirs {
		problems = append(problems, parallelProblems(t, dir, testFiles)...)
	}
	if len(problems) > 0 {
		t.Errorf("Standards parallel rule is not true:\n%s", strings.Join(problems, "\n"))
	}
}

// parallelProblems reports tests in one directory that disagree with the rule:
// a unit test calls t.Parallel unless it touches process-global state, and an
// integration test does not call it.
func parallelProblems(t *testing.T, dir string, testFiles []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	var testASTs []*ast.File
	for _, path := range testFiles {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files = append(files, file)
		testASTs = append(testASTs, file)
	}

	// Package variables live in the non-test files too. Assigning one from a
	// test is process-global state, same as t.Setenv.
	pkgFiles, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	for _, path := range pkgFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files = append(files, file)
	}

	pkgVars := packageVars(files)
	// Only helpers defined in tests count. A test reaches production code
	// through its public behaviour; that code is not a test swapping a package
	// variable.
	byName := map[string]*ast.FuncDecl{}
	for _, file := range testASTs {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Body != nil && fn.Recv == nil {
				byName[fn.Name.Name] = fn
			}
		}
	}

	var problems []string
	for _, file := range testASTs {
		requiresIntegration := fileRequiresIntegration(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !isTestFunc(fn) {
				continue
			}
			where := fset.Position(fn.Pos())
			touches := touchesProcessGlobal(fn, byName, pkgVars, map[string]bool{})
			parallel := callsParallel(fn)
			switch {
			case requiresIntegration && parallel:
				problems = append(problems, where.String()+": integration test calls t.Parallel; these tests share one hypervisor and fixed guest names")
			case !requiresIntegration && touches && parallel:
				problems = append(problems, where.String()+": calls t.Parallel and touches process-global state")
			case !requiresIntegration && !touches && !parallel:
				problems = append(problems, where.String()+": does not call t.Parallel")
			}
		}
	}
	return problems
}

func fileRequiresIntegration(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			text := strings.TrimSpace(comment.Text)
			if strings.HasPrefix(text, "//go:build") && strings.Contains(text, "integration") && !strings.Contains(text, "!integration") {
				return true
			}
		}
	}
	return false
}

func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "T"
}

func packageVars(files []*ast.File) map[string]bool {
	names := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.VAR {
				continue
			}
			for _, spec := range group.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					if name.Name != "_" {
						names[name.Name] = true
					}
				}
			}
		}
	}
	return names
}

func touchesProcessGlobal(fn *ast.FuncDecl, byName map[string]*ast.FuncDecl, pkgVars map[string]bool, seen map[string]bool) bool {
	if fn == nil || seen[fn.Name.Name] {
		return false
	}
	seen[fn.Name.Name] = true
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if callTouchesProcessGlobal(n) {
				found = true
			}
			ident, ok := n.Fun.(*ast.Ident)
			if ok && touchesProcessGlobal(byName[ident.Name], byName, pkgVars, seen) {
				found = true
			}
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if exprWritesPackageVar(lhs, pkgVars) {
					found = true
				}
			}
		case *ast.IncDecStmt:
			if exprWritesPackageVar(n.X, pkgVars) {
				found = true
			}
		}
		return true
	})
	return found
}

func callTouchesProcessGlobal(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, _ := sel.X.(*ast.Ident)
	switch sel.Sel.Name {
	case "Setenv":
		return true
	case "Unsetenv", "Chdir":
		return recv != nil && recv.Name == "os"
	case "SetDefault":
		return recv != nil && recv.Name == "slog"
	default:
		return false
	}
}

func exprWritesPackageVar(expr ast.Expr, pkgVars map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return pkgVars[e.Name]
	case *ast.StarExpr:
		return exprWritesPackageVar(e.X, pkgVars)
	case *ast.IndexExpr:
		return exprWritesPackageVar(e.X, pkgVars)
	case *ast.SelectorExpr:
		return exprWritesPackageVar(e.X, pkgVars)
	default:
		return false
	}
}

func callsParallel(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Parallel" {
			found = true
		}
		return true
	})
	return found
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}
