package codegen_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/sat0yu/sqlc-cobra/internal/codegen"
)

const fixtureDir = "testdata/mysql"

// ---- KebabCase ----

func TestKebabCase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"CountProjects", "count-projects"},
		{"GetDevice", "get-device"},
		{"ProjectID", "project-id"},
		{"DeleteProjectMember", "delete-project-member"},
		{"UpdateProjectName", "update-project-name"},
		{"ID", "id"},
		{"DSN", "dsn"},
		{"ownerID", "owner-id"},
		{"createdAt", "created-at"},
	}
	for _, tc := range cases {
		got := codegen.KebabCase(tc.in)
		if got != tc.want {
			t.Errorf("KebabCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---- LowerFirst ----

func TestLowerFirst(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CountProjects", "countProjects"},
		{"", ""},
		{"a", "a"},
		{"A", "a"},
	}
	for _, tc := range cases {
		if got := codegen.LowerFirst(tc.in); got != tc.want {
			t.Errorf("LowerFirst(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---- SafeIdent ----

func TestSafeIdent(t *testing.T) {
	reserved := []string{
		"type", "func", "range", "var", "const", "package", "import",
		"return", "switch", "case", "default", "if", "else", "for", "go",
		"defer", "select", "chan", "map", "struct", "interface", "break",
		"continue", "fallthrough", "goto",
	}
	for _, kw := range reserved {
		got := codegen.SafeIdent(kw)
		if got != kw+"_" {
			t.Errorf("SafeIdent(%q) = %q, want %q", kw, got, kw+"_")
		}
	}
	// Non-reserved identifiers pass through unchanged.
	for _, s := range []string{"foo", "bar", "id", "name", "params"} {
		if got := codegen.SafeIdent(s); got != s {
			t.Errorf("SafeIdent(%q) = %q, want %q", s, got, s)
		}
	}
}

// ---- EvalStringExpr (BinaryExpr concat) ----

// TestEvalStringExprAST tests EvalStringExpr directly using parsed AST nodes.
func TestEvalStringExprAST(t *testing.T) {
	src := `package p
const c1 = "hello world"
const c2 = "part1" + " part2"
const c3 = "a" + "` + "`" + `" + "b"
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "t.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		constIdx int
		want     string
	}{
		{0, "hello world"},
		{1, "part1 part2"},
		{2, "a`b"},
	}

	for i, tc := range cases {
		vs := f.Decls[i].(*ast.GenDecl).Specs[0].(*ast.ValueSpec)
		got, err := codegen.EvalStringExpr(vs.Values[0])
		if err != nil {
			t.Errorf("case %d EvalStringExpr error: %v", i, err)
			continue
		}
		if got != tc.want {
			t.Errorf("case %d = %q, want %q", i, got, tc.want)
		}
	}
}

// ---- ParseQueriesPackage ----

func TestParseQueriesPackage_Shapes(t *testing.T) {
	eng := &codegen.EngineMySQL
	methods, err := codegen.ParseQueriesPackage(
		fixtureDir,
		[]string{"Create", "Update", "Delete", "Insert", "Prune"},
		eng,
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	byName := map[string]codegen.Method{}
	for _, m := range methods {
		byName[m.Name] = m
	}

	t.Run("no-arg int return", func(t *testing.T) {
		m, ok := byName["CountProjects"]
		if !ok {
			t.Fatal("CountProjects not found")
		}
		if _, ok := m.Param.(codegen.NoArg); !ok {
			t.Errorf("param = %T, want NoArg", m.Param)
		}
		if _, ok := m.Ret.(codegen.IntReturn); !ok {
			t.Errorf("ret = %T, want IntReturn", m.Ret)
		}
		if m.Mutating {
			t.Error("CountProjects should not be mutating")
		}
	})

	t.Run("single-arg nil return mutating", func(t *testing.T) {
		m, ok := byName["DeleteProject"]
		if !ok {
			t.Fatal("DeleteProject not found")
		}
		if _, ok := m.Param.(codegen.SingleArg); !ok {
			t.Errorf("param = %T, want SingleArg", m.Param)
		}
		if _, ok := m.Ret.(codegen.NilReturn); !ok {
			t.Errorf("ret = %T, want NilReturn", m.Ret)
		}
		if !m.Mutating {
			t.Error("DeleteProject should be mutating")
		}
		if m.SQL == "" {
			t.Error("DeleteProject.SQL must not be empty")
		}
		if !strings.Contains(m.SQL, "DELETE") {
			t.Errorf("SQL does not contain DELETE: %s", m.SQL)
		}
	})

	t.Run("single-arg row return", func(t *testing.T) {
		m, ok := byName["GetProject"]
		if !ok {
			t.Fatal("GetProject not found")
		}
		sa, ok := m.Param.(codegen.SingleArg)
		if !ok {
			t.Errorf("param = %T, want SingleArg", m.Param)
		} else if sa.Name != "id" {
			t.Errorf("param name = %q, want id", sa.Name)
		}
		if _, ok := m.Ret.(codegen.RowReturn); !ok {
			t.Errorf("ret = %T, want RowReturn", m.Ret)
		}
	})

	t.Run("no-arg list return []struct", func(t *testing.T) {
		m, ok := byName["ListProjects"]
		if !ok {
			t.Fatal("ListProjects not found")
		}
		if _, ok := m.Ret.(codegen.ListReturn); !ok {
			t.Errorf("ret = %T, want ListReturn", m.Ret)
		}
	})

	t.Run("no-arg list return []string", func(t *testing.T) {
		m, ok := byName["ListProjectIDs"]
		if !ok {
			t.Fatal("ListProjectIDs not found")
		}
		lr, ok := m.Ret.(codegen.ListReturn)
		if !ok {
			t.Errorf("ret = %T, want ListReturn", m.Ret)
		} else if lr.TypeName != "string" {
			t.Errorf("TypeName = %q, want string", lr.TypeName)
		}
	})

	t.Run("struct-param execresult mutating", func(t *testing.T) {
		m, ok := byName["CreateProject"]
		if !ok {
			t.Fatal("CreateProject not found")
		}
		sp, ok := m.Param.(codegen.StructParam)
		if !ok {
			t.Errorf("param = %T, want StructParam", m.Param)
		} else {
			if sp.TypeName != "CreateProjectParams" {
				t.Errorf("TypeName = %q", sp.TypeName)
			}
			// Should have 4 fields: ID, Name, OwnerID, CreatedAt
			if len(sp.Fields) != 4 {
				t.Errorf("fields = %d, want 4", len(sp.Fields))
			}
		}
		if _, ok := m.Ret.(codegen.ExecResult); !ok {
			t.Errorf("ret = %T, want ExecResult", m.Ret)
		}
		if !m.Mutating {
			t.Error("CreateProject should be mutating")
		}
	})

	t.Run("struct-param nil return mutating with NullString", func(t *testing.T) {
		m, ok := byName["UpdateProjectName"]
		if !ok {
			t.Fatal("UpdateProjectName not found")
		}
		sp, ok := m.Param.(codegen.StructParam)
		if !ok {
			t.Errorf("param = %T, want StructParam", m.Param)
		} else {
			// Fields: Name (NullString), ID (string)
			var hasNullString bool
			for _, f := range sp.Fields {
				if f.Type == codegen.GoNullString {
					hasNullString = true
				}
			}
			if !hasNullString {
				t.Error("expected a GoNullString field in UpdateProjectNameParams")
			}
		}
		if _, ok := m.Ret.(codegen.NilReturn); !ok {
			t.Errorf("ret = %T, want NilReturn", m.Ret)
		}
	})

	t.Run("single-arg reserved-word param name", func(t *testing.T) {
		// GetProjectByType has param named "type_" in source but the
		// method param in the AST is "type_" (we named it that way in
		// the fixture). The SafeIdent escaping is tested in render.
		_, ok := byName["GetProjectByType"]
		if !ok {
			t.Fatal("GetProjectByType not found")
		}
	})
}

// ---- Render ----

func testConfig() codegen.Config {
	return codegen.Config{
		SrcDir:           fixtureDir,
		OutFile:          "",
		Pkg:              "sqlc",
		QueriesImport:    "github.com/example/app/internal/db/queries",
		Parent:           "SqlcCmd",
		Adapter:          "openQueries",
		RuntimeImport:    "github.com/sat0yu/sqlc-cobra/runtime",
		MutatingPrefixes: []string{"Create", "Update", "Delete", "Insert", "Prune"},
		Engine:           &codegen.EngineMySQL,
	}
}

func parseFixtureMethods(t *testing.T) []codegen.Method {
	t.Helper()
	cfg := testConfig()
	methods, err := codegen.ParseQueriesPackage(cfg.SrcDir, cfg.MutatingPrefixes, cfg.Engine)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return methods
}

func TestRenderIsValidGo(t *testing.T) {
	methods := parseFixtureMethods(t)
	cfg := testConfig()
	out, err := codegen.Render(methods, cfg)
	if err != nil {
		t.Fatalf("render: %v\n\n%s", err, out)
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "zz.go", out, 0); err != nil {
		t.Fatalf("output is not valid Go: %v\n\n%s", err, out)
	}
}

func TestRenderIdempotent(t *testing.T) {
	methods := parseFixtureMethods(t)
	cfg := testConfig()
	out1, err := codegen.Render(methods, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out2, err := codegen.Render(methods, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out1, out2) {
		t.Error("Render is not idempotent")
	}
}

func TestRenderContainsExpectedPatterns(t *testing.T) {
	methods := parseFixtureMethods(t)
	cfg := testConfig()
	out, err := codegen.Render(methods, cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	src := string(out)

	checks := []struct {
		name    string
		pattern string
	}{
		{"package decl", "package sqlc"},
		{"runtime import", `"github.com/sat0yu/sqlc-cobra/runtime"`},
		{"queries import", `"github.com/example/app/internal/db/queries"`},
		{"cobra import", `"github.com/spf13/cobra"`},
		{"CountProjects command", `"CountProjects"`},
		{"runtime.PrintInt64", "runtime.PrintInt64"},
		{"runtime.PrintJSON", "runtime.PrintJSON"},
		{"runtime.PrintOK", "runtime.PrintOK"},
		{"runtime.PrintExecResult", "runtime.PrintExecResult"},
		{"runtime.CmdContext", "runtime.CmdContext"},
		{"runtime.Prompt", "runtime.Prompt"},
		{"runtime.Stdout", "runtime.Stdout"},
		{"runtime.Stdin", "runtime.Stdin"},
		{"runtime.Stderr", "runtime.Stderr"},
		{"openQueries call", "openQueries()"},
		{"parent AddCommand", "SqlcCmd.AddCommand"},
		{"SQL const for Create", "createProjectSQL"},
		{"ConfirmMutation for Delete", "ConfirmMutation"},
		{"code-gen header", "Code generated by sqlc-cobra"},
	}

	for _, tc := range checks {
		if !strings.Contains(src, tc.pattern) {
			t.Errorf("missing %s: %q not found in output", tc.name, tc.pattern)
		}
	}

	// Generated code MUST NOT reference os.Std* directly — tests rely on
	// being able to swap runtime.Stdin/Stdout/Stderr to inject fake IO.
	for _, bad := range []string{"os.Stdin", "os.Stdout", "os.Stderr"} {
		if strings.Contains(src, bad) {
			t.Errorf("generated code unexpectedly contains %q (should reference runtime.Std* instead)", bad)
		}
	}
}

func TestRenderSafeIdentForReservedWord(t *testing.T) {
	methods := parseFixtureMethods(t)
	cfg := testConfig()
	out, err := codegen.Render(methods, cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	src := string(out)
	// The fixture GetProjectByType has a param named "type_" in source.
	// Verify the generated code does not contain a bare `type` local variable.
	if strings.Contains(src, "\ttype := ") || strings.Contains(src, "\ntype := ") {
		t.Error("generated code contains unescaped 'type' local variable")
	}
}
