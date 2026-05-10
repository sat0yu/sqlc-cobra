// Command sqlc-cobra-gen reads a sqlc-generated *Queries package and emits one
// cobra subcommand per method into a generated Go file.
//
// Usage:
//
//	sqlc-cobra-gen \
//	  -src  <queries-package-path>  \
//	  -out  <output-file-path>      \
//	  -pkg  <go-package-name>       \
//	  -queries-import <import-path> \
//	  -parent <var-name>
package main

import (
	"flag"
	"log"
	"strings"

	"github.com/sat0yu/sqlc-cobra/internal/codegen"
)

func main() {
	defaults := codegen.DefaultConfig()

	src := flag.String("src", "internal/db/queries", "filesystem path to the sqlc-generated queries package")
	out := flag.String("out", "zz_generated_commands.go", "output file path")
	pkg := flag.String("pkg", "sqlc", "Go package name for the output file")
	queriesImport := flag.String("queries-import", "", "import path of the sqlc queries package (required)")
	parent := flag.String("parent", "SqlcCmd", "parent cobra.Command variable name")
	adapter := flag.String("adapter", defaults.Adapter, "consumer-defined openQueries function name")
	runtimeImport := flag.String("runtime-import", defaults.RuntimeImport, "import path of the runtime helpers package")
	mutatingPrefixes := flag.String("mutating-prefixes", strings.Join(defaults.MutatingPrefixes, ","),
		"comma-separated list of method name prefixes that mark a method as mutating")
	engine := flag.String("engine", "mysql", "type registry to use: mysql")

	flag.Parse()

	if *queriesImport == "" {
		log.Fatal("sqlc-cobra-gen: -queries-import is required")
	}

	var prefixes []string
	for _, p := range strings.Split(*mutatingPrefixes, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			prefixes = append(prefixes, p)
		}
	}

	var eng *codegen.Engine
	switch *engine {
	case "mysql", "":
		eng = &codegen.EngineMySQL
	default:
		log.Fatalf("sqlc-cobra-gen: unknown engine %q (supported: mysql)", *engine)
	}

	cfg := codegen.Config{
		SrcDir:           *src,
		OutFile:          *out,
		Pkg:              *pkg,
		QueriesImport:    *queriesImport,
		Parent:           *parent,
		Adapter:          *adapter,
		RuntimeImport:    *runtimeImport,
		MutatingPrefixes: prefixes,
		Engine:           eng,
	}

	if err := codegen.Generate(cfg); err != nil {
		log.Fatalf("sqlc-cobra-gen: %v", err)
	}
}
