//go:generate go run ../../../../cmd/sqlc-cobra-gen -src ../../queries -pkg sqlc -queries-import github.com/sat0yu/sqlc-cobra/examples/mysql/queries -parent SqlcCmd -out zz_generated_commands.go

package sqlc

import "github.com/spf13/cobra"

// SqlcCmd is the parent cobra command. Register it with your root command.
var SqlcCmd = &cobra.Command{
	Use:   "sqlc",
	Short: "Invoke sqlc-generated queries directly from the CLI",
	Long: `Each sqlc-generated method on *Queries is exposed as a subcommand.
Mutating commands (Create/Delete) print the SQL with bound args and
prompt for confirmation; pass --yes to skip.`,
}

func init() {
	SqlcCmd.PersistentFlags().StringP("dsn", "d", "", "MySQL data source name")
	SqlcCmd.PersistentFlags().BoolP("yes", "y", false, "Skip the y/N prompt for mutating queries")
	_ = SqlcCmd.MarkPersistentFlagRequired("dsn")
}
