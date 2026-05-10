package runtime_test

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sat0yu/sqlc-cobra/runtime"
)

// ---- ErrAborted / IsAborted ----

func TestIsAborted(t *testing.T) {
	if !runtime.IsAborted(runtime.ErrAborted) {
		t.Fatal("IsAborted(ErrAborted) must be true")
	}
	if !runtime.IsAborted(fmt.Errorf("wrap: %w", runtime.ErrAborted)) {
		t.Fatal("IsAborted must unwrap through fmt.Errorf %w")
	}
	if runtime.IsAborted(errors.New("other")) {
		t.Fatal("IsAborted(other) must be false")
	}
}

// ---- ConfirmMutation ----

func newCmdWithYes(yes bool) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")
	if yes {
		_ = cmd.Flags().Set("yes", "true")
	}
	return cmd
}

func TestConfirmMutation_YesFlag(t *testing.T) {
	cmd := newCmdWithYes(true)
	p := runtime.Prompt{In: strings.NewReader(""), Out: io.Discard}
	if err := p.ConfirmMutation(cmd, "DELETE FROM x", nil); err != nil {
		t.Fatalf("--yes should bypass prompt: %v", err)
	}
}

func TestConfirmMutation_PromptAccept(t *testing.T) {
	for _, input := range []string{"y\n", "Y\n", "yes\n", "YES\n", "Yes\n"} {
		input := input
		t.Run(input, func(t *testing.T) {
			cmd := newCmdWithYes(false)
			p := runtime.Prompt{In: strings.NewReader(input), Out: io.Discard}
			if err := p.ConfirmMutation(cmd, "SELECT 1", nil); err != nil {
				t.Fatalf("input %q should accept, got: %v", input, err)
			}
		})
	}
}

func TestConfirmMutation_PromptReject(t *testing.T) {
	for _, input := range []string{"n\n", "N\n", "no\n", "\n", " \n"} {
		input := input
		t.Run(input, func(t *testing.T) {
			cmd := newCmdWithYes(false)
			p := runtime.Prompt{In: strings.NewReader(input), Out: io.Discard}
			err := p.ConfirmMutation(cmd, "DELETE FROM x", nil)
			if !runtime.IsAborted(err) {
				t.Fatalf("input %q should abort, got: %v", input, err)
			}
		})
	}
}

func TestConfirmMutation_EOF(t *testing.T) {
	cmd := newCmdWithYes(false)
	p := runtime.Prompt{In: strings.NewReader(""), Out: io.Discard}
	err := p.ConfirmMutation(cmd, "DELETE FROM x", nil)
	if !runtime.IsAborted(err) {
		t.Fatalf("EOF should abort, got: %v", err)
	}
}

func TestConfirmMutation_PrintsPreview(t *testing.T) {
	cmd := newCmdWithYes(true)
	var out bytes.Buffer
	p := runtime.Prompt{In: strings.NewReader(""), Out: &out}
	_ = p.ConfirmMutation(cmd, "DELETE FROM x WHERE id = ?", []any{"abc"})
	got := out.String()
	if !strings.Contains(got, "DELETE FROM x") {
		t.Errorf("expected SQL in output, got: %q", got)
	}
	if !strings.Contains(got, "abc") {
		t.Errorf("expected args in output, got: %q", got)
	}
}

// ---- PrintInt64 ----

func TestPrintInt64(t *testing.T) {
	var buf bytes.Buffer
	if err := runtime.PrintInt64(&buf, 42); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "42" {
		t.Errorf("got %q want %q", got, "42")
	}
}

// ---- PrintOK ----

func TestPrintOK(t *testing.T) {
	var buf bytes.Buffer
	if err := runtime.PrintOK(&buf); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "ok" {
		t.Errorf("got %q want %q", got, "ok")
	}
}

// ---- PrintJSON ----

type testRow struct {
	ID   string
	Name sql.NullString
	Age  sql.NullInt64
}

func TestPrintJSON_NullStringFlattening(t *testing.T) {
	row := testRow{
		ID:   "abc",
		Name: sql.NullString{String: "Alice", Valid: true},
		Age:  sql.NullInt64{Int64: 30, Valid: true},
	}
	var buf bytes.Buffer
	if err := runtime.PrintJSON(&buf, row); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `"Name": "Alice"`) {
		t.Errorf("NullString(Valid) should flatten to bare string; got: %s", got)
	}
	if !strings.Contains(got, `"Age": 30`) {
		t.Errorf("NullInt64(Valid) should flatten to bare int64; got: %s", got)
	}
}

func TestPrintJSON_NullStringNull(t *testing.T) {
	row := testRow{
		ID:   "abc",
		Name: sql.NullString{Valid: false},
		Age:  sql.NullInt64{Valid: false},
	}
	var buf bytes.Buffer
	if err := runtime.PrintJSON(&buf, row); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `"Name": null`) {
		t.Errorf("NullString(Invalid) should flatten to null; got: %s", got)
	}
	if !strings.Contains(got, `"Age": null`) {
		t.Errorf("NullInt64(Invalid) should flatten to null; got: %s", got)
	}
}

func TestPrintJSON_Slice(t *testing.T) {
	rows := []string{"a", "b", "c"}
	var buf bytes.Buffer
	if err := runtime.PrintJSON(&buf, rows); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, s := range rows {
		if !strings.Contains(got, `"`+s+`"`) {
			t.Errorf("expected %q in output, got: %s", s, got)
		}
	}
}

// ---- PrintExecResult ----

type fakeResult struct {
	lastID       int64
	rowsAffected int64
}

func (f fakeResult) LastInsertId() (int64, error) { return f.lastID, nil }
func (f fakeResult) RowsAffected() (int64, error) { return f.rowsAffected, nil }

func TestPrintExecResult(t *testing.T) {
	var buf bytes.Buffer
	r := fakeResult{lastID: 7, rowsAffected: 3}
	if err := runtime.PrintExecResult(&buf, r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `"lastInsertId": 7`) {
		t.Errorf("expected lastInsertId 7; got: %s", got)
	}
	if !strings.Contains(got, `"rowsAffected": 3`) {
		t.Errorf("expected rowsAffected 3; got: %s", got)
	}
}

// ---- CmdContext ----

func TestCmdContext_NoContext(t *testing.T) {
	cmd := &cobra.Command{}
	ctx := runtime.CmdContext(cmd)
	if ctx == nil {
		t.Fatal("CmdContext must never return nil")
	}
}

// ---- ParseXxx ----

func TestParseInt32(t *testing.T) {
	cases := []struct {
		in   string
		want int32
		err  bool
	}{
		{"42", 42, false},
		{"-1", -1, false},
		{"2147483647", 2147483647, false},
		{"2147483648", 0, true}, // overflow
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := runtime.ParseInt32(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("ParseInt32(%q) err=%v, wantErr=%v", tc.in, err, tc.err)
			continue
		}
		if !tc.err && got != tc.want {
			t.Errorf("ParseInt32(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseInt64(t *testing.T) {
	v, err := runtime.ParseInt64("9223372036854775807")
	if err != nil || v != 9223372036854775807 {
		t.Errorf("ParseInt64: got %d %v", v, err)
	}
	if _, err := runtime.ParseInt64("not-a-number"); err == nil {
		t.Error("ParseInt64 should error on non-numeric")
	}
}

func TestParseInt(t *testing.T) {
	v, err := runtime.ParseInt("100")
	if err != nil || v != 100 {
		t.Errorf("ParseInt: got %d %v", v, err)
	}
}

func TestParseBool(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"true", true}, {"1", true}, {"false", false}, {"0", false},
	} {
		got, err := runtime.ParseBool(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseBool(%q) = %v %v", tc.in, got, err)
		}
	}
	if _, err := runtime.ParseBool("maybe"); err == nil {
		t.Error("ParseBool should error on invalid input")
	}
}

func TestParseTime(t *testing.T) {
	ts := "2024-01-15T10:30:00Z"
	got, err := runtime.ParseTime(ts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Year() != 2024 {
		t.Errorf("ParseTime year = %d", got.Year())
	}
	if _, err := runtime.ParseTime("not-a-time"); err == nil {
		t.Error("ParseTime should error on invalid input")
	}
}

// ---- NullStringFromFlag ----

func TestNullStringFromFlag_NotSet(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("name", "", "")
	ns := runtime.NullStringFromFlag(cmd, "name")
	if ns.Valid {
		t.Error("unset flag should produce Valid=false")
	}
}

func TestNullStringFromFlag_Set(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("name", "", "")
	_ = cmd.Flags().Set("name", "Alice")
	ns := runtime.NullStringFromFlag(cmd, "name")
	if !ns.Valid || ns.String != "Alice" {
		t.Errorf("set flag should produce Valid=true String=Alice, got %+v", ns)
	}
}

func TestNullStringFromFlag_SetEmpty(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("name", "default", "")
	_ = cmd.Flags().Set("name", "")
	ns := runtime.NullStringFromFlag(cmd, "name")
	if !ns.Valid {
		t.Error("explicitly set-to-empty flag should produce Valid=true")
	}
}

// ---- package-level IO sink swap ----

func TestStdoutSwap_RedirectsPrint(t *testing.T) {
	old := runtime.Stdout
	defer func() { runtime.Stdout = old }()
	var buf bytes.Buffer
	runtime.Stdout = &buf

	// Generated commands call into runtime.PrintXxx with runtime.Stdout as
	// the writer. We simulate that pattern here and assert the swap routed
	// the bytes to our buffer instead of os.Stdout.
	if err := runtime.PrintInt64(runtime.Stdout, 42); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(buf.String()); got != "42" {
		t.Errorf("stdout swap did not capture output; got %q", got)
	}
}

func TestStdinSwap_FeedsPrompt(t *testing.T) {
	oldIn, oldOut := runtime.Stdin, runtime.Stderr
	defer func() { runtime.Stdin = oldIn; runtime.Stderr = oldOut }()
	runtime.Stdin = strings.NewReader("n\n")
	runtime.Stderr = io.Discard

	cmd := &cobra.Command{}
	cmd.Flags().Bool("yes", false, "")

	// Build the Prompt the same way the generated code does.
	err := runtime.Prompt{In: runtime.Stdin, Out: runtime.Stderr}.
		ConfirmMutation(cmd, "DELETE FROM x", []any{})
	if !runtime.IsAborted(err) {
		t.Fatalf("expected ErrAborted from canned 'n' input, got %v", err)
	}
}

// ---- time.Time in JSON (via PrintJSON) ----

func TestPrintJSON_TimeField(t *testing.T) {
	type rowWithTime struct {
		CreatedAt time.Time
	}
	ts, _ := time.Parse(time.RFC3339, "2024-06-01T00:00:00Z")
	var buf bytes.Buffer
	if err := runtime.PrintJSON(&buf, rowWithTime{CreatedAt: ts}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "2024-06-01") {
		t.Errorf("expected time in output; got: %s", got)
	}
}
