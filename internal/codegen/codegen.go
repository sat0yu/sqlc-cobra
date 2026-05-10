// Package codegen implements the AST walker and code generator that turns a
// sqlc-generated *Queries package into a tree of cobra subcommands.
package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"unicode"
)

// Version is embedded in the generated file header.
const Version = "v0.1.0"

// Config holds all consumer-supplied parameters for the generator.
type Config struct {
	// SrcDir is the filesystem path to the sqlc-generated queries package.
	SrcDir string

	// OutFile is the path where the generated file will be written.
	OutFile string

	// Pkg is the Go package name for the generated file (e.g. "sqlc").
	Pkg string

	// QueriesImport is the Go import path of the sqlc-generated queries package.
	QueriesImport string

	// Parent is the variable name of the parent cobra.Command (e.g. "SqlcCmd").
	Parent string

	// Adapter is the name of the consumer-defined openQueries function.
	// Defaults to "openQueries".
	Adapter string

	// RuntimeImport is the import path of the runtime helpers package.
	// Defaults to "github.com/sat0yu/sqlc-cobra/runtime".
	RuntimeImport string

	// MutatingPrefixes is the list of method name prefixes that classify a
	// method as mutating (triggers SQL preview + y/N prompt).
	// Defaults to ["Create", "Update", "Delete", "Insert", "Prune"].
	MutatingPrefixes []string

	// Engine selects the built-in type registry.  Currently only EngineMySQL
	// is defined; this field is reserved for future engines.
	Engine *Engine
}

// DefaultConfig returns a Config with sensible defaults applied.
func DefaultConfig() Config {
	return Config{
		Adapter:          "openQueries",
		RuntimeImport:    "github.com/sat0yu/sqlc-cobra/runtime",
		MutatingPrefixes: []string{"Create", "Update", "Delete", "Insert", "Prune"},
		Engine:           &EngineMySQL,
	}
}

// Generate runs the full pipeline: parse → classify → render → write.
func Generate(cfg Config) error {
	if cfg.Adapter == "" {
		cfg.Adapter = "openQueries"
	}
	if cfg.RuntimeImport == "" {
		cfg.RuntimeImport = "github.com/sat0yu/sqlc-cobra/runtime"
	}
	if len(cfg.MutatingPrefixes) == 0 {
		cfg.MutatingPrefixes = []string{"Create", "Update", "Delete", "Insert", "Prune"}
	}
	if cfg.Engine == nil {
		cfg.Engine = &EngineMySQL
	}

	methods, err := ParseQueriesPackage(cfg.SrcDir, cfg.MutatingPrefixes, cfg.Engine)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })

	buf, err := Render(methods, cfg)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.OutFile), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(cfg.OutFile, buf, 0o644)
}

// ---- type model ----

// Method represents one *Queries method that will become a cobra command.
type Method struct {
	Name     string
	Param    ParamShape
	Ret      RetShape
	SQL      string
	Mutating bool
}

// ParamShape is implemented by noArg, SingleArg, StructParam.
type ParamShape interface{ paramShape() }

// NoArg: method takes only context.Context.
type NoArg struct{}

// SingleArg: method takes context.Context + one primitive argument.
type SingleArg struct {
	Name string // lowerCamel identifier from source
	Type GoType
}

// StructParam: method takes context.Context + one FooParams struct.
type StructParam struct {
	TypeName string
	Fields   []ParamField
}

type ParamField struct {
	Name string // PascalCase field name
	Type GoType
}

func (NoArg) paramShape()       {}
func (SingleArg) paramShape()   {}
func (StructParam) paramShape() {}

// RetShape is implemented by IntReturn, NilReturn, RowReturn, ListReturn, ExecResult.
type RetShape interface{ retShape() }

type IntReturn struct{}
type NilReturn struct{}
type RowReturn struct{ TypeName string }
type ListReturn struct{ TypeName string }
type ExecResult struct{}

func (IntReturn) retShape()  {}
func (NilReturn) retShape()  {}
func (RowReturn) retShape()  {}
func (ListReturn) retShape() {}
func (ExecResult) retShape() {}

// ---- type registry ----

// GoType is a recognised type in the sqlc-generated queries package.
type GoType int

const (
	GoString GoType = iota
	GoInt32
	GoInt64
	GoInt
	GoBool
	GoTime
	GoNullString
)

func (t GoType) String() string {
	switch t {
	case GoString:
		return "string"
	case GoInt32:
		return "int32"
	case GoInt64:
		return "int64"
	case GoInt:
		return "int"
	case GoBool:
		return "bool"
	case GoTime:
		return "time.Time"
	case GoNullString:
		return "sql.NullString"
	}
	return "?"
}

// Engine bundles a named type registry. The Engine field in Config selects
// which one to use; callers can construct a custom Engine for non-standard
// type sets.
type Engine struct {
	Name string
	// typeFromExpr maps a Go type expression string to a GoType.
	typeFromExpr map[string]GoType
}

// EngineMySQL is the default engine for MySQL-backed sqlc packages.
var EngineMySQL = Engine{
	Name: "mysql",
	typeFromExpr: map[string]GoType{
		"string":         GoString,
		"int32":          GoInt32,
		"int64":          GoInt64,
		"int":            GoInt,
		"bool":           GoBool,
		"time.Time":      GoTime,
		"sql.NullString": GoNullString,
	},
}

func (e *Engine) lookup(expr string) (GoType, bool) {
	t, ok := e.typeFromExpr[expr]
	return t, ok
}

// ---- AST parsing ----

// ParseQueriesPackage parses all *.sql.go files in dir and returns the
// classified methods.
func ParseQueriesPackage(dir string, mutatingPrefixes []string, eng *Engine) ([]Method, error) {
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
				m, ok, err := classifyMethod(fn, structDefs, constDefs, mutatingPrefixes, eng)
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
				val, err := EvalStringExpr(vs.Values[0])
				if err != nil {
					continue
				}
				consts[vs.Names[0].Name] = val
			}
		}
	}
}

func classifyMethod(fn *ast.FuncDecl, structs map[string]*ast.StructType, consts map[string]string, mutatingPrefixes []string, eng *Engine) (Method, bool, error) {
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

	var pShape ParamShape = NoArg{}
	switch len(params) {
	case 1:
		// no extra args
	case 2:
		extra := params[1]
		// Distinguish struct param from primitive.
		if id, ok := extra.typeExpr.(*ast.Ident); ok {
			if st, found := structs[id.Name]; found {
				fields, err := readStructFields(st, eng)
				if err != nil {
					return Method{}, false, fmt.Errorf("%s: %w", fn.Name.Name, err)
				}
				pShape = StructParam{TypeName: id.Name, Fields: fields}
				break
			}
		}
		gt, err := goTypeOf(extra.typeExpr, eng)
		if err != nil {
			return Method{}, false, fmt.Errorf("%s param %s: %w", fn.Name.Name, extra.name, err)
		}
		pShape = SingleArg{Name: extra.name, Type: gt}
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

	sqlText := ""
	if mutating {
		constName := LowerFirst(fn.Name.Name)
		val, ok := consts[constName]
		if !ok {
			return Method{}, false, fmt.Errorf("%s: SQL const %q not found in queries package", fn.Name.Name, constName)
		}
		sqlText = val
	}

	return Method{
		Name:     fn.Name.Name,
		Param:    pShape,
		Ret:      rShape,
		SQL:      sqlText,
		Mutating: mutating,
	}, true, nil
}

type fieldInfo struct {
	name     string
	typeExpr ast.Expr
}

// flattenFields turns *ast.FieldList into one entry per identifier so that
// `arg X, Y string` -> two entries.
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

func readStructFields(st *ast.StructType, eng *Engine) ([]ParamField, error) {
	fields := flattenFields(st.Fields)
	out := make([]ParamField, 0, len(fields))
	for _, f := range fields {
		gt, err := goTypeOf(f.typeExpr, eng)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", f.name, err)
		}
		out = append(out, ParamField{Name: f.name, Type: gt})
	}
	return out, nil
}

func goTypeOf(expr ast.Expr, eng *Engine) (GoType, error) {
	key := typeExprString(expr)
	if t, ok := eng.lookup(key); ok {
		return t, nil
	}
	return 0, fmt.Errorf("unsupported type %q", key)
}

// typeExprString converts an ast.Expr to the canonical string that the engine
// type registry uses as keys (e.g. "string", "sql.NullString", "time.Time").
func typeExprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		pkg, _ := t.X.(*ast.Ident)
		if pkg == nil {
			return ""
		}
		return pkg.Name + "." + t.Sel.Name
	}
	return fmt.Sprintf("%T", expr)
}

func classifyReturn(results []fieldInfo) (RetShape, error) {
	switch len(results) {
	case 1:
		if isErrorType(results[0].typeExpr) {
			return NilReturn{}, nil
		}
	case 2:
		if !isErrorType(results[1].typeExpr) {
			return nil, fmt.Errorf("second return must be error, got %T", results[1].typeExpr)
		}
		first := results[0].typeExpr
		// (int64, error)
		if id, ok := first.(*ast.Ident); ok && id.Name == "int64" {
			return IntReturn{}, nil
		}
		// (sql.Result, error)
		if sel, ok := first.(*ast.SelectorExpr); ok {
			pkg, _ := sel.X.(*ast.Ident)
			if pkg != nil && pkg.Name == "sql" && sel.Sel.Name == "Result" {
				return ExecResult{}, nil
			}
		}
		// ([]string, error) or ([]Foo, error)
		if arr, ok := first.(*ast.ArrayType); ok && arr.Len == nil {
			elemName, err := typeNameOf(arr.Elt)
			if err != nil {
				return nil, fmt.Errorf("list element: %w", err)
			}
			return ListReturn{TypeName: elemName}, nil
		}
		// (Foo, error) — single struct or named primitive
		name, err := typeNameOf(first)
		if err != nil {
			return nil, fmt.Errorf("row: %w", err)
		}
		return RowReturn{TypeName: name}, nil
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

// EvalStringExpr evaluates a const-expression of string literals concatenated
// with +.  sqlc emits `…` + "`" + `…` to embed backticks, producing a
// BinaryExpr rather than a plain BasicLit.
func EvalStringExpr(expr ast.Expr) (string, error) {
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
		l, err := EvalStringExpr(e.X)
		if err != nil {
			return "", err
		}
		r, err := EvalStringExpr(e.Y)
		if err != nil {
			return "", err
		}
		return l + r, nil
	case *ast.ParenExpr:
		return EvalStringExpr(e.X)
	}
	return "", fmt.Errorf("unsupported expr %T", expr)
}

// LowerFirst lowercases the first rune of s.
func LowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// KebabCase converts PascalCase or camelCase to kebab-case.
// "ProjectID" → "project-id", "GetDevice" → "get-device".
func KebabCase(s string) string {
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

// SafeIdent appends an underscore to names that are Go reserved words so
// that generated code is valid. E.g., a struct field named "Type" becomes
// the local variable "type_".
func SafeIdent(name string) string {
	switch name {
	case "break", "case", "chan", "const", "continue", "default", "defer",
		"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
		"interface", "map", "package", "range", "return", "select", "struct",
		"switch", "type", "var":
		return name + "_"
	}
	return name
}

// ---- rendering ----

// runtimeAlias returns the short alias used in the generated file to refer to
// the runtime package (last path segment of the import path).
func runtimeAlias(importPath string) string {
	parts := strings.Split(importPath, "/")
	return parts[len(parts)-1]
}

// queriesAlias returns the short alias for the queries import.
func queriesAlias(importPath string) string {
	parts := strings.Split(importPath, "/")
	return parts[len(parts)-1]
}

// MethodView is the template input. All fields are exported so text/template
// can access them.
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

	Single      SingleArg
	Struct      StructParam
	StructAlias string // e.g. "queries.FooParams"

	IsIntReturn  bool
	IsNilReturn  bool
	IsRowReturn  bool
	IsListReturn bool
	IsExec       bool

	ArgsValidator string
}

func buildMethodView(m Method, cfg Config) MethodView {
	qAlias := queriesAlias(cfg.QueriesImport)
	v := MethodView{
		Name:     m.Name,
		VarName:  LowerFirst(m.Name) + "Cmd",
		UseLine:  m.Name,
		Short:    "Calls Queries." + m.Name,
		Mutating: m.Mutating,
		SQLConst: LowerFirst(m.Name) + "SQL",
		SQL:      m.SQL,
	}

	switch p := m.Param.(type) {
	case NoArg:
		v.IsNoArg = true
		v.ArgsValidator = "cobra.ExactArgs(0)"
	case SingleArg:
		v.IsSingleArg = true
		v.Single = p
		v.UseLine = m.Name + " <" + p.Name + ">"
		v.ArgsValidator = "cobra.ExactArgs(1)"
	case StructParam:
		v.IsStruct = true
		v.Struct = p
		v.StructAlias = qAlias + "." + p.TypeName
		v.ArgsValidator = "cobra.ExactArgs(0)"
	}

	switch m.Ret.(type) {
	case IntReturn:
		v.IsIntReturn = true
	case NilReturn:
		v.IsNilReturn = true
	case RowReturn:
		v.IsRowReturn = true
	case ListReturn:
		v.IsListReturn = true
	case ExecResult:
		v.IsExec = true
	}
	return v
}

func goCallArg(v MethodView) string {
	switch {
	case v.IsNoArg:
		return ""
	case v.IsSingleArg:
		return ", " + SafeIdent(v.Single.Name)
	case v.IsStruct:
		return ", params"
	}
	return ""
}

func flagDeclCode(f ParamField) string {
	flag := KebabCase(f.Name)
	switch f.Type {
	case GoString:
		return fmt.Sprintf(`cmd.Flags().String(%q, "", %q)`+"\n\t"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case GoInt32:
		return fmt.Sprintf(`cmd.Flags().Int32(%q, 0, %q)`+"\n\t"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case GoInt64:
		return fmt.Sprintf(`cmd.Flags().Int64(%q, 0, %q)`+"\n\t"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case GoInt:
		return fmt.Sprintf(`cmd.Flags().Int(%q, 0, %q)`+"\n\t"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case GoBool:
		return fmt.Sprintf(`cmd.Flags().Bool(%q, false, %q)`+"\n\t"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case GoTime:
		return fmt.Sprintf(`cmd.Flags().String(%q, "", %q+" (RFC3339)")`+"\n\t"+
			`cmd.MarkFlagRequired(%q)`, flag, f.Name, flag)
	case GoNullString:
		// NullString flags are optional — omitting means SQL NULL.
		return fmt.Sprintf(`cmd.Flags().String(%q, "", %q+" (omit for NULL)")`, flag, f.Name)
	}
	return "/* unsupported field type */"
}

func flagReadCode(f ParamField, rAlias string) string {
	flag := KebabCase(f.Name)
	local := SafeIdent(LowerFirst(f.Name))
	switch f.Type {
	case GoString:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetString(%q)`, local, flag)
	case GoInt32:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetInt32(%q)`, local, flag)
	case GoInt64:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetInt64(%q)`, local, flag)
	case GoInt:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetInt(%q)`, local, flag)
	case GoBool:
		return fmt.Sprintf(`%s, _ := cmd.Flags().GetBool(%q)`, local, flag)
	case GoTime:
		return fmt.Sprintf(
			`%sStr, _ := cmd.Flags().GetString(%q)`+"\n\t\t\t"+
				`%s, errParse := %s.ParseTime(%sStr)`+"\n\t\t\t"+
				`if errParse != nil { return fmt.Errorf("--%s: %%w", errParse) }`,
			local, flag, local, rAlias, local, flag)
	case GoNullString:
		return fmt.Sprintf(`%s := %s.NullStringFromFlag(cmd, %q)`, local, rAlias, flag)
	}
	return "/* unsupported */"
}

// Render produces the formatted Go source for the generated commands file.
func Render(methods []Method, cfg Config) ([]byte, error) {
	rAlias := runtimeAlias(cfg.RuntimeImport)
	qAlias := queriesAlias(cfg.QueriesImport)

	// Build file header.
	var header bytes.Buffer
	fmt.Fprintf(&header, "// Code generated by sqlc-cobra %s (https://github.com/sat0yu/sqlc-cobra)\n", Version)
	fmt.Fprintf(&header, "// DO NOT EDIT.\n\n")
	fmt.Fprintf(&header, "package %s\n\n", cfg.Pkg)
	fmt.Fprintf(&header, "import (\n")
	fmt.Fprintf(&header, "\t\"fmt\"\n\n")
	fmt.Fprintf(&header, "\t%q\n", cfg.QueriesImport)
	fmt.Fprintf(&header, "\t\"github.com/spf13/cobra\"\n")
	fmt.Fprintf(&header, "\t%s %q\n", rAlias, cfg.RuntimeImport)
	fmt.Fprintf(&header, ")\n\n")
	fmt.Fprintf(&header, "var _ = fmt.Sprint  // keep fmt referenced\n")
	fmt.Fprintf(&header, "var _ = %s.New      // keep %s referenced\n\n", qAlias, qAlias)

	tmpl, err := template.New("cmd").Funcs(template.FuncMap{
		"lowerFirst": LowerFirst,
		"kebabCase":  KebabCase,
		"goCallArg":  goCallArg,
		"goParseLine": func(t GoType, src string) string {
			return goParseLine(t, src, rAlias)
		},
		"flagDecl": flagDeclCode,
		"flagRead": func(f ParamField) string {
			return flagReadCode(f, rAlias)
		},
		"goLit":     strconv.Quote,
		"safeLocal": func(s string) string { return SafeIdent(LowerFirst(s)) },
		"rAlias":    func() string { return rAlias },
		"adapter":   func() string { return cfg.Adapter },
		"parent":    func() string { return cfg.Parent },
	}).Parse(commandTemplate)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(header.Bytes())

	for _, m := range methods {
		v := buildMethodView(m, cfg)
		if err := tmpl.Execute(&buf, v); err != nil {
			return nil, fmt.Errorf("%s: %w", m.Name, err)
		}
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return buf.Bytes(), fmt.Errorf("gofmt: %w\n--- output ---\n%s", err, buf.String())
	}
	return formatted, nil
}

func goParseLine(t GoType, src, rAlias string) string {
	switch t {
	case GoString:
		return src
	case GoInt32:
		return rAlias + ".ParseInt32(" + src + ")"
	case GoInt64:
		return rAlias + ".ParseInt64(" + src + ")"
	case GoInt:
		return rAlias + ".ParseInt(" + src + ")"
	case GoBool:
		return rAlias + ".ParseBool(" + src + ")"
	case GoTime:
		return rAlias + ".ParseTime(" + src + ")"
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
			ctx := {{ rAlias }}.CmdContext(cmd)
			q, cleanup, err := {{ adapter }}()
			if err != nil { return err }
			defer cleanup()

			{{- if .IsSingleArg }}
			{{- if eq .Single.Type.String "string" }}
			{{ safeLocal .Single.Name }} := args[0]
			{{- else }}
			{{ safeLocal .Single.Name }}, err := {{ goParseLine .Single.Type "args[0]" }}
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
			if err := ({{ rAlias }}.Prompt{In: {{ rAlias }}.Stdin, Out: {{ rAlias }}.Stderr}).ConfirmMutation(cmd, {{ .SQLConst }}, []any{
				{{- if .IsSingleArg -}}
				{{ safeLocal .Single.Name }},
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
			return {{ rAlias }}.PrintInt64({{ rAlias }}.Stdout, n)
			{{- else if .IsNilReturn }}
			if err := q.{{ .Name }}(ctx{{ goCallArg . }}); err != nil { return err }
			return {{ rAlias }}.PrintOK({{ rAlias }}.Stdout)
			{{- else if .IsRowReturn }}
			row, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return {{ rAlias }}.PrintJSON({{ rAlias }}.Stdout, row)
			{{- else if .IsListReturn }}
			rows, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return {{ rAlias }}.PrintJSON({{ rAlias }}.Stdout, rows)
			{{- else if .IsExec }}
			res, err := q.{{ .Name }}(ctx{{ goCallArg . }})
			if err != nil { return err }
			return {{ rAlias }}.PrintExecResult({{ rAlias }}.Stdout, res)
			{{- end }}
		},
	}
	{{- if .IsStruct }}
	{{- range .Struct.Fields }}
	{{ flagDecl . }}
	{{- end }}
	{{- end }}
	{{ parent }}.AddCommand(cmd)
}
`
