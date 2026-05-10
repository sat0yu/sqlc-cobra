package sqlc

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"

	"github.com/sat0yu/sqlc-cobra/examples/mysql/queries"
)

// openQueries opens a MySQL connection using the --dsn flag and returns a
// *Queries handle plus a cleanup function.  The function signature is fixed:
// the generated commands expect exactly (*queries.Queries, func(), error).
func openQueries() (*queries.Queries, func(), error) {
	dsn, err := SqlcCmd.Flags().GetString("dsn")
	if err != nil || dsn == "" {
		// Fall back to PersistentFlags (the flag lives on the parent).
		dsn, _ = SqlcCmd.PersistentFlags().GetString("dsn")
	}
	if dsn == "" {
		return nil, func() {}, fmt.Errorf("--dsn is required")
	}
	db, err := sql.Open("mysql", dsn+"?parseTime=true")
	if err != nil {
		return nil, func() {}, fmt.Errorf("open db: %w", err)
	}
	cleanup := func() { _ = db.Close() }
	return queries.New(db), cleanup, nil
}

// rootCmd wires SqlcCmd to cobra's Execute.
var rootCmd = &cobra.Command{
	Use:   "example",
	Short: "sqlc-cobra example CLI",
}

func init() {
	rootCmd.AddCommand(SqlcCmd)
}

// Execute is the example entry point.
func Execute() error {
	return rootCmd.Execute()
}
