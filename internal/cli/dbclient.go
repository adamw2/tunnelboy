package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/adamw2/tunnelboy/internal/tui"
)

// runDBClient launches the appropriate database CLI against the local tunnel
// port, passing the IAM token as the password via environment variable so it
// never appears in the process list. Blocks until the client exits.
func runDBClient(engine, dbUser, dbName string, localPort int, token string) error {
	var binary string
	var args []string
	var env []string

	switch {
	case strings.Contains(engine, "postgres"):
		binary = "psql"
		args = []string{"-h", "127.0.0.1", "-p", fmt.Sprintf("%d", localPort), "-U", dbUser}
		if dbName != "" {
			args = append(args, "-d", dbName)
		}
		// RDS IAM auth requires SSL; "require" skips hostname verification,
		// which would fail against localhost anyway.
		env = []string{"PGPASSWORD=" + token, "PGSSLMODE=require"}

	case strings.Contains(engine, "mysql") || strings.Contains(engine, "mariadb"):
		binary = "mysql"
		args = []string{
			"-h", "127.0.0.1",
			"-P", fmt.Sprintf("%d", localPort),
			"-u", dbUser,
			"--enable-cleartext-plugin", // required for RDS IAM auth
			"--ssl-mode=REQUIRED",
		}
		if dbName != "" {
			args = append(args, "-D", dbName)
		}
		env = []string{"MYSQL_PWD=" + token}

	default:
		return fmt.Errorf("--exec is not supported for engine %q (postgres and mysql families only)", engine)
	}

	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("%s not found on PATH. Install it or connect manually with the details above", binary)
	}

	fmt.Printf("%s Launching %s...\n\n", tui.DimStyle.Render("►"), binary)

	cmd := exec.Command(binary, args...) // #nosec G204 -- fixed binary name, args built from validated flags, no shell
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
