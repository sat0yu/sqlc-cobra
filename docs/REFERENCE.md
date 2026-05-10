# Reference Implementation

The full source of the seed implementation from a Go service.
It is correct, working, and tested against MySQL in production. This is
the starting point for the library extraction — adapt these files into
the layout in [DESIGN.md](DESIGN.md), parameterising the three
seed-specific bits noted in section 3 below.

Files in this document:

1. `generator/main.go` — the AST walker + cobra template (~780 lines)
2. `helpers.go` — runtime helpers (~210 lines)
3. `sqlc.go` — parent cobra command + `go:generate` directive
4. Sample generated output — three representative methods
5. Test patterns — how the helpers were tested

## 1. seed-specific bits to parameterise

When adapting these files into the library layout, three things change:

| In the seed | What the library needs |
|---|---|
| `package sqlc` | `-pkg <name>` flag, default `sqlc` |
| `github.com/example/app/internal/db/queries` import | `-queries-import` flag, no default |
| `SqlcCmd.AddCommand(cmd)` | `-parent <var>` flag, default `SqlcCmd` |

Additionally, the seed's `openQueries()`, `printInt64`, `printJSON`,
`confirmMutation`, etc. are unqualified package-internal calls. In the
library version they become `runtime.Foo(...)` calls and the consumer
provides their own `openQueries()`.

## 2. Generator source (full file)

```go
// Command sqlccli scrapes the sqlc-generated *Queries methods under
// internal/db/queries and emits one cobra subcommand per method into
// cmd/sqlc/zz_generated_commands.go.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"
)

func main() {
	src := flag.String("src", "internal/db/queries", "path to the sqlc-generated queries package")
	out := flag.String("out", "cmd/sqlc/zz_generated_commands.go", "output path for the generated file")
	flag.Parse()

	methods, err := parseQueriesPackage(*src)
	if err != nil {
		log.Fatalf("sqlccli: parse: %v", err)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })

	buf, err := render(methods)
	if err != nil {
		log.Fatalf("sqlccli: render: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatalf("sqlccli: mkdir: %v", err)
	}
	if err := os.WriteFile(*out, buf, 0o644); err != nil {
		log.Fatalf("sqlccli: write: %v", err)
	}
}

// Method is one *Queries method that the generator will turn into a cobra command.
type Method struct {
	Name     string
	Param    paramShape
	Ret      retShape
	SQL      string
	Mutating bool
}

type paramShape interface{ paramShape() }

type noArg struct{}

type singleArg struct {
	Name string // param identifier as written in source (lowerCamel)
	Type goType
}

type structParam struct {
	TypeName string // e.g. "DeleteProjectMemberParams"
	Fields   []paramField
}

type paramField struct {
	Name string // e.g. "ProjectID"
	Type goType
}

func (noArg) paramShape()       {}
func (singleArg) paramShape()   {}
func (structParam) paramShape() {}

type retShape interface{ retShape() }

type intReturn struct{}                   // (int64, error)
type nilReturn struct{}                   // error
type rowReturn struct{ TypeName string }  // (Foo, error)
type listReturn struct{ TypeName string } // ([]Foo or []string, error)
type execResult struct{}                  // (sql.Result, error)

func (intReturn) retShape()  {}
func (nilReturn) retShape()  {}
func (rowReturn) retShape()  {}
func (listReturn) retShape() {}
func (execResult) retShape() {}

type goType int

const (
	goString goType = iota
	goInt32
	goInt64
	goInt
	goBool
	goTime
	goNullString
)

func (t goType) String() string {
	switch t {
	case goString:
		return "string"
	case goInt32:
		return "int32"
	case goInt64:
		return "int64"
	case goInt:
		return "int"
	case goBool:
		return "bool"
	case goTime:
		return "time.Time"
	case goNullString:
		return "sql.NullString"
	}
	return "?"
}

var mutatingPrefixes = []string{"Create", "Update", "Delete", "Insert", "Prune"}

func parseQueriesPackage(dir string) ([]Method, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".sql.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no Go packages found in %s", dir)
	}

	structDefs := map[string]*ast.StructType{}
	constDefs := map[string]string{}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			collectTypesAndConsts(file, structDefs, constDefs)
		}
	}

	var methods []Method
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				m, ok, err := classifyMethod(fn, structDefs, constDefs)
				if err != nil {
					return nil, fmt.Errorf("classify %s: %w", fn.Name.Name, err)
				}
				if !ok {
					continue
				}
				methods = append(methods, m)
			}
		}
	}
	return methods, nil
}

func collectTypesAndConsts(file *ast.File, structs map[string]*ast.StructType, consts map[string]string) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		switch gen.Tok {
		case token.TYPE:
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				structs[ts.Name.Name] = st
			}
		case token.CONST:
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				val, err := evalStringExpr(vs.Values[0])
				if err != nil {
					continue
				}
				consts[vs.Names[0].Name] = val
			}
		}
	}
}

func classifyMethod(fn *ast.FuncDecl, structs map[string]*ast.StructType, consts map[string]string) (Method, bool, error) {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return Method{}, false, nil
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return Method{}, false, nil
	}
	recvIdent, ok := star.X.(*ast.Ident)
	if !ok || recvIdent.Name != "Queries" {
		return Method{}, false, nil
	}
	if !fn.Name.IsExported() {
		return Method{}, false, nil
	}

	params := flattenFields(fn.Type.Params)
	if len(params) < 1 {
		return Method{}, false, nil
	}

	// First param must be context.Context.
	ctxType, ok := params[0].typeExpr.(*ast.SelectorExpr)
	if !ok {
		return Method{}, false, nil
	}
	pkg, _ := ctxType.X.(*ast.Ident)
	if pkg == nil || pkg.Name != "context" || ctxType.Sel.Name != "Context" {
		return Method{}, false, nil
	}

	var pShape paramShape = noArg{}
	switch len(params) {
	case 1:
		// no extra args
	case 2:
		extra := params[1]
		// Distinguish struct param from primitive.
		if id, ok := extra.typeExpr.(*ast.Ident); ok {
			if st, found := structs[id.Name]; found {
				fields, err := readStructFields(st)
				if err != nil {
					return Method{}, false, fmt.Errorf("%s: %w", fn.Name.Name, err)
				}
				pShape = structParam{TypeName: id.Name, Fields: fields}
				break
			}
		}
		gt, err := goTypeOf(extra.typeExpr)
		if err != nil {
			return Method{}, false, fmt.Errorf("%s param %s: %w", fn.Name.Name, extra.name, err)
		}
		pShape = singleArg{Name: extra.name, Type: gt}
	default:
		return Method{}, false, fmt.Errorf("%s: unsupported parameter count %d", fn.Name.Name, len(params))
	}

	results := flattenFields(fn.Type.Results)
	rShape, err := classifyReturn(results)
	if err != nil {
		return Method{}, false, fmt.Errorf("%s return: %w", fn.Name.Name, err)
	}

	mutating := false
	for _, p := range mutatingPrefixes {
		if strings.HasPrefix(fn.Name.Name, p) {
			mutating = true
			break
		}
	}

	sql := ""
	if mutating {
		constName := lowerFirst(fn.Name.Name)
		val, ok := consts[constName]
		if !ok {
			return Method{}, false, fmt.Errorf("%s: SQL const %q not found in queries package", fn.Name.Name, constName)
		}
		sql = val
	}

	return Method{
		Name:     fn.Name.Name,
		Param:    pShape,
		Ret:      rShape,
		SQL:      sql,
		Mutating: mutating,
	}, true, nil
}

type fieldInfo struct {
	name     string
	typeExpr ast.Expr
}

// flattenFields turns *ast.FieldList into one entry per identifier so that
// `arg X, Y string` -> two fields.
func flattenFields(fl *ast.FieldList) []fieldInfo {
	var out []fieldInfo
	if fl == nil {
		return out
	}
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			out = append(out, fieldInfo{name: "", typeExpr: f.Type})
			continue
		}
		for _, n := range f.Names {
			out = append(out, fieldInfo{name: n.Name, typeExpr: f.Type})
		}
	}
	return out
}

func readStructFields(st *ast.StructType) ([]paramField, error) {
	fields := flattenFields(st.Fields)
	out := make([]paramField, 0, len(fields))
	for _, f := range fields {
		gt, err := goTypeOf(f.typeExpr)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.name, err)
		}
		out = append(out, paramField{Name: f.name, Type: gt})
	}
	return out, nil
}

func goTypeOf(expr ast.Expr) (goType, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return goString, nil
		case "int32":
			return goInt32, nil
		case "int64":
			return goInt64, nil
		case "int":
			return goInt, nil
		case "bool":
			return goBool, nil
		}
	case *ast.SelectorExpr:
		pkg, _ := t.X.(*ast.Ident)
		if pkg == nil {
			break
		}
		switch pkg.Name + "." + t.Sel.Name {
		case "time.Time":
			return goTime, nil
		case "sql.NullString":
			return goNullString, nil
		}
	}
	return 0, fmt.Errorf("unsupported type %T", expr)
}

func classifyReturn(results []fieldInfo) (retShape, error) {
	switch len(results) {
	case 1:
		// just error
		if isErrorType(results[0].typeExpr) {
			return nilReturn{}, nil
		}
	case 2:
		if !isErrorType(results[1].typeExpr) {
			return nil, fmt.Errorf("second return must be error, got %T", results[1].typeExpr)
		}
		first := results[0].typeExpr
		// (int64, error)
		if id, ok := first.(*ast.Ident); ok && id.Name == "int64" {
			return intReturn{}, nil
		}
		// (sql.Result, error)
		if sel, ok := first.(*ast.SelectorExpr); ok {
			pkg, _ := sel.X.(*ast.Ident)
			if pkg != nil && pkg.Name == "sql" && sel.Sel.Name == "Result" {
				return execResult{}, nil
			}
		}
		// ([]string, error) or ([]Foo, error)
		if arr, ok := first.(*ast.ArrayType); ok && arr.Len == nil {
			elemName, err := typeNameOf(arr.Elt)
			if err != nil {
				return nil, fmt.Errorf("list element: %w", err)
			}
			return listReturn{TypeName: elemName}, nil
		}
		// (Foo, error) — single struct or named primitive
		name, err := typeNameOf(first)
		if err != nil {
			return nil, fmt.Errorf("row: %w", err)
		}
		return rowReturn{TypeName: name}, nil
	}
	return nil, fmt.Errorf("unsupported result count %d", len(results))
}

func isErrorType(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "error"
}

func typeNameOf(expr ast.Expr) (string, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name, nil
	case *ast.SelectorExpr:
		pkg, _ := t.X.(*ast.Ident)
		if pkg == nil {
			return "", fmt.Errorf("unsupported selector %T", t.X)
		}
		return pkg.Name + "." + t.Sel.Name, nil
	}
	return "", fmt.Errorf("unsupported type expr %T", expr)
}

// evalStringExpr evaluates a const-expression of string literals concatenated
// with `+`. sqlc emits `` `...` + "`" + `...` `` to embed backticks.
func evalStringExpr(expr ast.Expr) (string, error) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", fmt.Errorf("not a string literal: %v", e.Kind)
		}
		return strconv.Unquote(e.Value)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", fmt.Errorf("unsupported op %v", e.Op)
		}
		l, err := evalStringExpr(e.X)
		if err != nil {
			return "", err
		}
		r, err := evalStringExpr(e.Y)
		if err != nil {
			return "", err
		}
		return l + r, nil
	case *ast.ParenExpr:
		return evalStringExpr(e.X)
	}
	return "", fmt.Errorf("unsupported expr %T", expr)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func kebabCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				var next rune
				hasNext := i+1 < len(runes)
				if hasNext {
					next = runes[i+1]
				}
				if unicode.IsLower(prev) || (unicode.IsUpper(prev) && hasNext && unicode.IsLower(next)) {
					b.WriteRune('-')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ---- rendering ----

const fileHeader = `// Code generated by internal/gen/sqlccli; DO NOT EDIT.

package sqlc

import (
	"fmt"

	"github.com/example/app/internal/db/queries"
	"github.com/spf13/cobra"
)

var _ = fmt.Sprint  // keep fmt referenced for blocks that don't use it
var _ = queries.New // ensure the import is used

`

func render(methods []Method) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(fileHeader)

	tmpl, err := template.New("cmd").Funcs(template.FuncMap{
		"lowerFirst":  lowerFirst,
		"kebabCase":   kebabCase,
		"goCallArg":   goCallArg,
		"goParseLine": goParseLine,
		"flagDecl":    flagDecl,
		"flagRead":    flagRead,
		"goLit":       strconv.Quote,
		"safeLocal":   func(s string) string { return safeIdent(lowerFirst(s)) },
	}).Parse(commandTemplate)
	if err != nil {
		return nil, err
	}

	for _, m := range methods {
		if err := tmpl.Execute(&buf, methodView(m)); err != nil {
			return nil, fmt.Errorf("%s: %w", m.Name, err)
		}
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return buf.Bytes(), fmt.Errorf("gofmt: %w\n--- output ---\n%s", err, buf.String())
	}
	return formatted, nil
}

// MethodView is the template input. Fields are exported so the template can read them.
type MethodView struct {
	Name     string
	VarName  string
	UseLine  string
	Short    string
	Mutating bool
	SQLConst string
	SQL      string

	IsNoArg     bool
	IsSingleArg bool
	IsStruct    bool

	Single      singleArg
	Struct      structParam
	StructAlias string

	IsIntReturn  bool
	IsNilReturn  bool
	IsRowReturn  bool
	IsListReturn bool
	IsExec       bool

	ArgsValidator string
}

func methodView(m Method) MethodView {
	v := MethodView{
		Name:     m.Name,
		VarName:  lowerFirst(m.Name) + "Cmd",
		UseLine:  m.Name,
		Short:    "Calls Queries." + m.Name,
		Mutating: m.Mutating,
		SQLConst: lowerFirst(m.Name) + "SQL",
		SQL:      m.SQL,
	}

	switch p := m.Param.(type) {
	case noArg:
		v.IsNoArg = true
		v.ArgsValidator = "cobra.ExactArgs(0)"
	case singleArg:
		v.IsSingleArg = true
		v.Single = p
		v.UseLine = m.Name + " <" + p.Name + ">"
		v.ArgsValidator = "cobra.ExactArgs(1)"
	case structParam:
		v.IsStruct = true
		v.Struct = p
		v.StructAlias = "queries." + p.TypeName
		v.ArgsValidator = "cobra.ExactArgs(0)"
	}

	switch m.Ret.(type) {
	case intReturn:
		v.IsIntReturn = true
	case nilReturn:
		v.IsNilReturn = true
	case rowReturn:
		v.IsRowReturn = true
	case listReturn:
		v.IsListReturn = true
	case execResult:
		v.IsExec = true
	}
	return v
}

func goCallArg(v MethodView) string {
	switch {
	case v.IsNoArg:
		return ""
	case v.IsSingleArg:
		return ", " + safeIdent(v.Single.Name)
	case v.IsStruct:
		return ", params"
	}
	return ""
}

func safeIdent(name string) string {
	switch name {
	case "type", "func", "range", "var", "const", "package", "import",
		"return", "switch", "case", "default", "if", "else", "for", "go",
		"defer", "select", "chan", "map", "struct", "interface", "break",
		"continue", "fallthrough", "goto":
		return name + "_"
	}
	return name
}

func goParseLine(t goType, src string) string {
	switch t {
	case goString:
		return src
	case goInt32:
		return "parseInt32(" + src + ")"
	case goInt64:
		return "parseInt64(" + src + ")"
	case goInt:
		return "parseInt(" + src + ")"
	case goBool:
		return "parseBool(" + src + ")"
	case goTime:
		return "parseTime(" + src + ")"
	}
	return "/* unsupported */"
}

func flagDecl(f paramField) string {
	flag := kebabCase(f.Name)
	switch f.Type {
	case goString:
		return fmt.Sprintf(`cmd.Flags().String(%q, "", %q)`+"\n"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case goInt32:
		return fmt.Sprintf(`cmd.Flags().Int32(%q, 0, %q)`+"\n"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case goInt64:
		return fmt.Sprintf(`cmd.Flags().Int64(%q, 0, %q)`+"\n"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case goInt:
		return fmt.Sprintf(`cmd.Flags().Int(%q, 0, %q)`+"\n"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case goBool:
		return fmt.Sprintf(`cmd.Flags().Bool(%q, false, %q)`+"\n"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case goTime:
		return fmt.Sprintf(`cmd.Flags().String(%q, "", %q+" (RFC3339)")`+"\n"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case goNullString:
		return fmt.Sprintf(`cmd.Flags().String(%q, "", %q+" (omit for NULL)")`,
			flag, f.Name)
	}
	return "/* unsupported field type */"
}

func flagRead(f paramField) string {
	flag := kebabCase(f.Name)
	local := safeIdent(lowerFirst(f.Name))
	switch f.Type {
	case goString:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetString(%q)`, local, flag)
	case goInt32:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetInt32(%q)`, local, flag)
	case goInt64:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetInt64(%q)`, local, flag)
	case goInt:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetInt(%q)`, local, flag)
	case goBool:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetBool(%q)`, local, flag)
	case goTime:
		return fmt.Sprintf(`%sStr, _ := cmd.Flags().GetString(%q)
%s, errParse := parseTime(%sStr)
if errParse != nil { return fmt.Errorf("--%s: %%w", errParse) }`,
			local, flag, local, local, flag)
	case goNullString:
		return fmt.Sprintf(`%s := nullStringFromFlag(cmd, %q)`, local, flag)
	}
	return "/* unsupported */"
}

const commandTemplate = `
{{- if .Mutating }}
const {{.SQLConst}} = {{ goLit .SQL }}
{{- end }}

func init() {
	cmd := &cobra.Command{
		Use:   {{ goLit .UseLine }},
		Short: {{ goLit .Short }},
		Args:  {{ .ArgsValidator }},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			q, cleanup, err := openQueries()
			if err != nil { return err }
			defer cleanup()

			{{- if .IsSingleArg }}
			{{- if eq .Single.Type.String "string" }}
			{{ .Single.Name }} := args[0]
			{{- else }}
			{{ .Single.Name }}, err := {{ goParseLine .Single.Type "args[0]" }}
			if err != nil { return fmt.Errorf("{{ .Single.Name }}: %w", err) }
			{{- end }}
			{{- end }}

			{{- if .IsStruct }}
			{{- range .Struct.Fields }}
			{{ flagRead . }}
			{{- end }}
			params := {{ $.StructAlias }}{
				{{- range .Struct.Fields }}
				{{.Name}}: {{ safeLocal .Name }},
				{{- end }}
			}
			{{- end }}

			{{- if .Mutating }}
			if err := confirmMutation(cmd, {{ .SQLConst }}, []any{
				{{- if .IsSingleArg -}}
				{{ .Single.Name }},
				{{- else if .IsStruct -}}
				{{- range .Struct.Fields -}}
				params.{{.Name}},
				{{- end -}}
				{{- end -}}
			}); err != nil { return err }
			{{- end }}

			{{- if .IsIntReturn }}
			n, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return printInt64(n)
			{{- else if .IsNilReturn }}
			if err := q.{{ .Name }}(ctx{{ goCallArg . }}); err != nil { return err }
			return printOK()
			{{- else if .IsRowReturn }}
			row, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return printJSON(row)
			{{- else if .IsListReturn }}
			rows, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return printJSON(rows)
			{{- else if .IsExec }}
			res, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return printExecResult(res)
			{{- end }}
		},
	}
	{{- if .IsStruct }}
	{{- range .Struct.Fields }}
	{{ flagDecl . }}
	{{- end }}
	{{- end }}
	SqlcCmd.AddCommand(cmd)
}
`
```

## 3. Helpers source (full file)

```go
package sqlc

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/example/app/internal/db"
	"github.com/example/app/internal/db/queries"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// promptIn and promptOut are overridable in tests.
var (
	promptIn  io.Reader = os.Stdin
	promptOut io.Writer = os.Stderr
	stdoutW   io.Writer = os.Stdout
)

// errAborted is returned when the user declines a mutation prompt.
var errAborted = errors.New("aborted by user")

// openQueries opens a MySQL connection using --dsn and returns a Queries
// handle plus a cleanup function. Does NOT call db.Ping (left to first use).
func openQueries() (*queries.Queries, func(), error) {
	dsn := viper.GetString("dsn")
	if dsn == "" {
		return nil, func() {}, errors.New("--dsn is required")
	}
	conn := db.NewDB(db.Conf{
		DriverName:     "mysql",
		DataSourceName: dsn,
		QueryString:    "?parseTime=true",
	})
	cleanup := func() { _ = conn.Close() }
	return queries.New(conn), cleanup, nil
}

// confirmMutation prints the SQL preview and bound args to stderr, and
// (unless --yes was passed) reads y/N from stdin. Anything other than
// y/Y/yes/YES aborts with errAborted.
func confirmMutation(cmd *cobra.Command, sqlText string, args []any) error {
	fmt.Fprintf(promptOut, "SQL: %s\n", sqlText)
	fmt.Fprintf(promptOut, "args: %v\n", args)
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}
	fmt.Fprint(promptOut, "Proceed? [y/N]: ")
	reader := bufio.NewReader(promptIn)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return errAborted
	}
}

// IsAborted reports whether err is the user-aborted-mutation sentinel.
func IsAborted(err error) bool { return errors.Is(err, errAborted) }

// printInt64 prints n followed by a newline.
func printInt64(n int64) error {
	_, err := fmt.Fprintln(stdoutW, n)
	return err
}

// printOK prints "ok" followed by a newline.
func printOK() error {
	_, err := fmt.Fprintln(stdoutW, "ok")
	return err
}

// printJSON marshals v with sql.NullString flattened, two-space indent.
func printJSON(v any) error {
	cleaned := cleanValue(reflect.ValueOf(v))
	out, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdoutW, string(out))
	return err
}

// printExecResult formats sql.Result as JSON. Uses a map (not a tagged
// struct) because cleanValue keys off Go field names, not JSON tags.
func printExecResult(r sql.Result) error {
	li, err := r.LastInsertId()
	if err != nil {
		return fmt.Errorf("LastInsertId: %w", err)
	}
	ra, err := r.RowsAffected()
	if err != nil {
		return fmt.Errorf("RowsAffected: %w", err)
	}
	out, err := json.MarshalIndent(map[string]int64{
		"lastInsertId": li,
		"rowsAffected": ra,
	}, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdoutW, string(out))
	return err
}

// cleanValue recursively converts a value to a JSON-friendly form, in
// particular flattening sql.NullString to bare string-or-nil.
func cleanValue(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Struct:
		if ns, ok := rv.Interface().(sql.NullString); ok {
			if ns.Valid {
				return ns.String
			}
			return nil
		}
		m := map[string]any{}
		rt := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			m[f.Name] = cleanValue(rv.Field(i))
		}
		return m
	case reflect.Slice, reflect.Array:
		items := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			items[i] = cleanValue(rv.Index(i))
		}
		return items
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return cleanValue(rv.Elem())
	default:
		return rv.Interface()
	}
}

func parseInt32(s string) (int32, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

func parseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func parseInt(s string) (int, error) {
	n, err := strconv.ParseInt(s, 10, 0)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func parseBool(s string) (bool, error) { return strconv.ParseBool(s) }

func parseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

func nullStringFromFlag(cmd *cobra.Command, flag string) sql.NullString {
	if !cmd.Flags().Changed(flag) {
		return sql.NullString{Valid: false}
	}
	v, _ := cmd.Flags().GetString(flag)
	return sql.NullString{String: v, Valid: true}
}

// cmdContext returns cmd.Context() when set (by ExecuteContext), otherwise
// context.Background. The seed root uses plain Execute, which does not
// inject a context — so the generated code routes through this helper.
func cmdContext(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}
```

## 4. Parent command + `go:generate` directive

```go
//go:generate go run ../../internal/gen/sqlccli -src=../../internal/db/queries -out=zz_generated_commands.go

package sqlc

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var SqlcCmd = &cobra.Command{
	Use:   "sqlc",
	Short: "Invoke sqlc-generated queries directly from the CLI",
	Long: `Each sqlc-generated method on *queries.Queries is exposed as a
subcommand. Mutating commands (Create/Update/Delete/Insert/Prune) print
the SQL with bound args and prompt for confirmation; pass --yes to skip.`,
}

func init() {
	SqlcCmd.PersistentFlags().StringP("dsn", "d", "", "Data source name (mysql)")
	SqlcCmd.PersistentFlags().BoolP("yes", "y", false, "Skip the y/N prompt for mutating queries")
	_ = SqlcCmd.MarkPersistentFlagRequired("dsn")
	_ = viper.BindPFlag("dsn", SqlcCmd.PersistentFlags().Lookup("dsn"))
}
```

## 5. Sample generated output (representative methods)

The full output for the seed (45 commands) is ~1370 lines. Three
representatives are shown — pick one of each shape when testing the
library generator's output.

### 5.1 No-arg, int return (CountProjects)

```go
func init() {
	cmd := &cobra.Command{
		Use:   "CountProjects",
		Short: "Calls Queries.CountProjects",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			q, cleanup, err := openQueries()
			if err != nil {
				return err
			}
			defer cleanup()
			n, err := q.CountProjects(ctx)
			if err != nil {
				return err
			}
			return printInt64(n)
		},
	}
	SqlcCmd.AddCommand(cmd)
}
```

### 5.2 Single-primitive arg, row return (GetDevice)

```go
func init() {
	cmd := &cobra.Command{
		Use:   "GetDevice <id>",
		Short: "Calls Queries.GetDevice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			q, cleanup, err := openQueries()
			if err != nil {
				return err
			}
			defer cleanup()
			id := args[0]
			row, err := q.GetDevice(ctx, id)
			if err != nil {
				return err
			}
			return printJSON(row)
		},
	}
	SqlcCmd.AddCommand(cmd)
}
```

### 5.3 Struct-param, exec-result, mutating (CreateProjectCertificate)

```go
const createProjectCertificateSQL = "-- name: CreateProjectCertificate :execresult\nINSERT INTO `project_certificates` (\n  `project_id`, `pem`, `arn`, `created_at`, `updated_at`\n) VALUES (\n  ?, ?, ?, now(6), now(6)\n)\n"

func init() {
	cmd := &cobra.Command{
		Use:   "CreateProjectCertificate",
		Short: "Calls Queries.CreateProjectCertificate",
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			q, cleanup, err := openQueries()
			if err != nil {
				return err
			}
			defer cleanup()
			projectID, _ := cmd.Flags().GetString("project-id")
			pem, _ := cmd.Flags().GetString("pem")
			arn, _ := cmd.Flags().GetString("arn")
			params := queries.CreateProjectCertificateParams{
				ProjectID: projectID,
				Pem:    pem,
				Arn:    arn,
			}
			if err := confirmMutation(cmd, createProjectCertificateSQL, []any{params.ProjectID, params.Pem, params.Arn}); err != nil {
				return err
			}
			res, err := q.CreateProjectCertificate(ctx, params)
			if err != nil {
				return err
			}
			return printExecResult(res)
		},
	}
	cmd.Flags().String("project-id", "", "ProjectID")
	cmd.MarkFlagRequired("project-id")
	cmd.Flags().String("pem", "", "Pem")
	cmd.MarkFlagRequired("pem")
	cmd.Flags().String("arn", "", "Arn")
	cmd.MarkFlagRequired("arn")
	SqlcCmd.AddCommand(cmd)
}
```

## 6. Test patterns (selected)

### 6.1 Generator: classify each shape against the real queries package

```go
func TestParseRealQueries(t *testing.T) {
	methods, err := parseQueriesPackage("../../db/queries")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(methods) < 40 {
		t.Fatalf("expected >=40 methods, got %d", len(methods))
	}

	byName := map[string]Method{}
	for _, m := range methods {
		byName[m.Name] = m
	}

	t.Run("no-arg int return", func(t *testing.T) {
		m := byName["CountProjects"]
		if _, ok := m.Param.(noArg); !ok { t.Errorf("param = %T", m.Param) }
		if _, ok := m.Ret.(intReturn); !ok { t.Errorf("ret = %T", m.Ret) }
		if m.Mutating { t.Error("CountProjects should not be mutating") }
	})

	t.Run("struct-param mutating", func(t *testing.T) {
		m := byName["DeleteProjectMember"]
		sp := m.Param.(structParam)
		// asserts field order, types, mutating=true, SQL has "DELETE FROM"
	})

	// ...one subtest per shape (single-primitive, int32-arg, nullable-field,
	// error-only return, []string list return, WithTx-excluded)
}
```

### 6.2 Generator: render output is valid Go and idempotent

```go
func TestRenderRealQueriesIsValidGoAndIdempotent(t *testing.T) {
	methods, _ := parseQueriesPackage("../../db/queries")
	out1, _ := render(methods)
	out2, _ := render(methods)
	if !bytes.Equal(out1, out2) {
		t.Error("render is not idempotent")
	}
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "zz.go", out1, 0); err != nil {
		t.Fatalf("output is not valid Go: %v", err)
	}
	// spot-check known call sites exist in the output
}
```

### 6.3 Helpers: confirmMutation against fake stdin

```go
func TestConfirmMutation_PromptYes(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	prevIn, prevOut := promptIn, promptOut
	defer func() { promptIn, promptOut = prevIn, prevOut }()
	promptIn = strings.NewReader("y\n")
	promptOut = io.Discard

	if err := confirmMutation(cmd, "DELETE FROM x", []any{}); err != nil {
		t.Fatalf("y should accept: %v", err)
	}
}

func TestConfirmMutation_PromptNoAborts(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	promptIn = strings.NewReader("n\n")
	promptOut = io.Discard
	defer /* restore */

	err := confirmMutation(cmd, "DELETE FROM x", []any{})
	if !errors.Is(err, errAborted) {
		t.Fatalf("expected errAborted, got %v", err)
	}
}
```

### 6.4 Integration: drive the generated command against a real DB

The seed used `testutils.IntegrationTestRunner` (dockertest + MySQL 8) in a
sub-package. In the library, examples/<engine>/ does the equivalent.

```go
func TestSqlcCLI_CountProjects(t *testing.T) {
	db, teardown := testutils.GetIntegrationTestDBConn()
	defer teardown()
	ctx := context.Background()

	queries := q.New(db)
	for _, id := range []string{"proj-a", "proj-b"} {
		queries.CreateProject(ctx, q.CreateProjectParams{ID: id, ...})
	}

	out, err := runCmd(t, testutils.GetIntegrationTestDSN(), "", []string{"CountProjects"})
	if err != nil { t.Fatalf("CountProjects: %v", err) }
	if got := strings.TrimSpace(out); got != "2" {
		t.Errorf("stdout = %q, want 2", got)
	}
}
```

## 7. What broke during the seed's development

These are real bugs that surfaced and how they were fixed. Useful for
predicting where the library tests should be tight.

| Symptom | Root cause | Fix |
|---|---|---|
| Empty `const createProjectCertificateSQL = ""` in generated output | The const value was an `ast.BinaryExpr` (sqlc embedding backticks via `+ "\`" +`), not a `BasicLit` | Add `evalStringExpr` that recursively walks `BinaryExpr` |
| `gofmt` failure on generated code: `expected 'IDENT', found ','` | A param struct field named `Type` → local var `type` (reserved word) | Add `safeIdent(...)` to escape Go keywords |
| Generated code panics calling `q.X(nil, ...)` | `cmd.Context()` returns nil under plain `Execute()` | Add `cmdContext(cmd)` helper |
| `printExecResult` printed `LastInsertID` instead of `lastInsertId` | `cleanValue` keys on Go field names; JSON tags are ignored | Use `map[string]int64` instead of tagged struct |
| `go generate` failed with "no such file or directory" for `internal/db/queries` | `go generate` runs in the directive's source dir, not repo root | Pin paths in the directive: `-src=../../internal/db/queries` |
| All helper unit tests failed locally without Docker | `TestMain` for the integration test gates the whole binary | Move integration tests to a sibling sub-package |

The library's tests should cover at least the first four (they're
shape-level invariants, not seed-specific). Items 5 and 6 are usage
gotchas — document them in the consumer-facing README rather than
testing.
