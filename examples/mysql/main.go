package main

import (
	"fmt"
	"os"

	sqlccmd "github.com/sat0yu/sqlc-cobra/examples/mysql/cmd/sqlc"
)

func main() {
	if err := sqlccmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
