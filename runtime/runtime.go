// Package runtime provides the helpers that sqlc-cobra-generated commands call
// at runtime. It is intentionally kept small and has no transitive dependency
// on any database driver.
package runtime

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

	"github.com/spf13/cobra"
)

// Stdin, Stdout, and Stderr are the IO sinks the generated commands use for
// prompt input, normal output, and prompt preview / error output respectively.
// They default to os.Stdin / os.Stdout / os.Stderr but can be reassigned in
// tests to capture or inject content:
//
//	old := runtime.Stdout
//	runtime.Stdout = buf
//	defer func() { runtime.Stdout = old }()
//
// These are package-level globals — tests that swap them must not run in
// parallel with other tests that swap them. For programmatic library use
// (constructing a Prompt or calling PrintXxx by hand), prefer passing the
// io.Writer / io.Reader directly.
var (
	Stdin  io.Reader = os.Stdin
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr
)

// ErrAborted is returned when the user declines a mutation prompt.
var ErrAborted = errors.New("aborted by user")

// IsAborted reports whether err is the user-aborted-mutation sentinel.
func IsAborted(err error) bool { return errors.Is(err, ErrAborted) }

// Prompt holds the IO sinks for the mutation confirmation flow.
// Construct one per invocation so tests can pass fake readers/writers
// without racing on shared package-level state.
type Prompt struct {
	In  io.Reader // typically os.Stdin
	Out io.Writer // typically os.Stderr (prompt text + SQL preview)
}

// ConfirmMutation prints the SQL preview and bound args to p.Out, then
// (unless --yes was passed on cmd) reads y/N from p.In. Anything other
// than y/Y/yes/YES aborts with ErrAborted.
func (p Prompt) ConfirmMutation(cmd *cobra.Command, sqlText string, args []any) error {
	fmt.Fprintf(p.Out, "SQL: %s\n", sqlText)
	fmt.Fprintf(p.Out, "args: %v\n", args)
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}
	fmt.Fprint(p.Out, "Proceed? [y/N]: ")
	reader := bufio.NewReader(p.In)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return ErrAborted
	}
}

// PrintInt64 writes n followed by a newline to w.
func PrintInt64(w io.Writer, n int64) error {
	_, err := fmt.Fprintln(w, n)
	return err
}

// PrintOK writes "ok" followed by a newline to w.
func PrintOK(w io.Writer) error {
	_, err := fmt.Fprintln(w, "ok")
	return err
}

// PrintJSON marshals v with sql.NullString flattened to bare value-or-null,
// using two-space indent.
func PrintJSON(w io.Writer, v any) error {
	cleaned := cleanValue(reflect.ValueOf(v))
	out, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// PrintExecResult formats a sql.Result as JSON with lastInsertId and
// rowsAffected fields. Uses a map literal (not a tagged struct) so that
// cleanValue — which keys on Go field names, not JSON tags — preserves
// the intended camelCase keys.
func PrintExecResult(w io.Writer, r sql.Result) error {
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
	_, err = fmt.Fprintln(w, string(out))
	return err
}

// cleanValue recursively converts a value to a JSON-friendly form,
// flattening sql.NullString (and other sql.Null* types via the nullableSQL
// interface) to bare value-or-nil.
func cleanValue(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Struct:
		// time.Time implements json.Marshaler; let it pass through as-is.
		if _, ok := rv.Interface().(time.Time); ok {
			return rv.Interface()
		}
		// Flatten sql.NullString → bare string or nil.
		if ns, ok := rv.Interface().(sql.NullString); ok {
			if ns.Valid {
				return ns.String
			}
			return nil
		}
		// Flatten sql.NullInt64 → bare int64 or nil.
		if ni, ok := rv.Interface().(sql.NullInt64); ok {
			if ni.Valid {
				return ni.Int64
			}
			return nil
		}
		// Flatten sql.NullInt32 → bare int32 or nil.
		if ni, ok := rv.Interface().(sql.NullInt32); ok {
			if ni.Valid {
				return ni.Int32
			}
			return nil
		}
		// Flatten sql.NullBool → bare bool or nil.
		if nb, ok := rv.Interface().(sql.NullBool); ok {
			if nb.Valid {
				return nb.Bool
			}
			return nil
		}
		// Flatten sql.NullFloat64 → bare float64 or nil.
		if nf, ok := rv.Interface().(sql.NullFloat64); ok {
			if nf.Valid {
				return nf.Float64
			}
			return nil
		}
		// Flatten sql.NullTime → bare time.Time or nil.
		if nt, ok := rv.Interface().(sql.NullTime); ok {
			if nt.Valid {
				return nt.Time
			}
			return nil
		}
		// General struct: walk exported fields, key on Go field name.
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

// CmdContext returns cmd.Context() when set (via ExecuteContext), otherwise
// context.Background(). cobra.Command.Execute does not inject a context, so
// generated code always routes through this helper rather than calling
// cmd.Context() directly.
func CmdContext(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

// ParseInt32 parses a decimal string as int32.
func ParseInt32(s string) (int32, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// ParseInt64 parses a decimal string as int64.
func ParseInt64(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

// ParseInt parses a decimal string as int.
func ParseInt(s string) (int, error) {
	n, err := strconv.ParseInt(s, 10, 0)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// ParseBool parses a boolean string ("true", "false", "1", "0", etc.).
func ParseBool(s string) (bool, error) { return strconv.ParseBool(s) }

// ParseTime parses an RFC3339 timestamp string.
func ParseTime(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// NullStringFromFlag returns a sql.NullString from a cobra string flag.
// If the flag was not explicitly set on the command line, Valid is false
// (representing SQL NULL). If it was set (even to ""), Valid is true.
func NullStringFromFlag(cmd *cobra.Command, flag string) sql.NullString {
	if !cmd.Flags().Changed(flag) {
		return sql.NullString{Valid: false}
	}
	v, _ := cmd.Flags().GetString(flag)
	return sql.NullString{String: v, Valid: true}
}
