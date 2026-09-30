package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/hub"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/storeopen"
)

// hashPassword prints the argon2id hash of a password read from stdin: with
// a prompt and no echo on a terminal, or the first line of piped input.
func hashPassword(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: eddy-hub hash-password < password")
		return 2
	}
	var pw string
	if fd := int(stdin.Fd()); term.IsTerminal(fd) {
		fmt.Fprint(stderr, "Password: ")
		a, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			fmt.Fprintln(stderr, "eddy-hub: read password:", err)
			return 1
		}
		fmt.Fprint(stderr, "Again: ")
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			fmt.Fprintln(stderr, "eddy-hub: read password:", err)
			return 1
		}
		if string(a) != string(b) {
			fmt.Fprintln(stderr, "eddy-hub: passwords do not match")
			return 1
		}
		pw = string(a)
	} else {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, "eddy-hub: read password:", err)
			return 1
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(stderr, "eddy-hub:", err)
		return 1
	}
	fmt.Fprintln(stdout, h)
	return 0
}

const adminUsage = `usage:
  eddy-hub admin revoke --user <subject> [--config /etc/eddy/hub.yaml]
      Delete every session and revoke every token of a user (e.g. local:alice).
  eddy-hub admin backup --out <file> [--config /etc/eddy/hub.yaml]
      Write a consistent copy of the SQLite store (VACUUM INTO).
`

func admin(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("eddy-hub admin "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", defaultConfig, "path to hub.yaml")
	var user, out string
	switch cmd {
	case "revoke":
		fs.StringVar(&user, "user", "", "subject to revoke, e.g. local:alice")
	case "backup":
		fs.StringVar(&out, "out", "", "backup file to create")
	default:
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	cfg, err := config.LoadHub(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "eddy-hub:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	log := slog.New(slog.NewJSONHandler(stderr, nil))

	switch cmd {
	case "revoke":
		if user == "" {
			fmt.Fprintln(stderr, "eddy-hub: --user is required")
			return 2
		}
		if err := revoke(ctx, cfg, user, log); err != nil {
			fmt.Fprintln(stderr, "eddy-hub:", err)
			return 1
		}
		fmt.Fprintf(stdout, "revoked all sessions and tokens of %s\n", user)
	case "backup":
		if out == "" {
			fmt.Fprintln(stderr, "eddy-hub: --out is required")
			return 2
		}
		if cfg.Store.Driver != storeopen.DriverSQLite {
			fmt.Fprintf(stderr, "eddy-hub: backup needs the sqlite store (driver is %q)\n", cfg.Store.Driver)
			return 1
		}
		if err := hub.BackupSQLite(ctx, cfg.Store.Path, out); err != nil {
			fmt.Fprintln(stderr, "eddy-hub:", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", out)
	}
	return 0
}

// revoke works on the store directly, so it needs neither the hub key nor
// the users file. The running hub sees the change on the next request.
func revoke(ctx context.Context, cfg *config.Hub, subject string, log *slog.Logger) error {
	if cfg.Store.Driver == storeopen.DriverMemory {
		return errors.New("the memory store lives inside the running hub; restart it instead")
	}
	st, err := storeopen.Open(ctx, cfg.Store, log)
	if err != nil {
		return err
	}
	defer st.Close()
	var errs []error
	if err := st.Sessions().DeleteBySubject(ctx, subject); err != nil {
		errs = append(errs, fmt.Errorf("delete sessions: %w", err))
	}
	if err := st.Tokens().RevokeBySubject(ctx, subject, time.Now().UTC()); err != nil {
		errs = append(errs, fmt.Errorf("revoke tokens: %w", err))
	}
	res := store.AuditOK
	if len(errs) > 0 {
		res = store.AuditError
	}
	audit.New(st.Audit(), log).Record(ctx, identity.Principal{User: "admin", Via: identity.ViaSystem},
		"user.revoke", store.ResourceRef{}, res, map[string]string{"subject": subject})
	return errors.Join(errs...)
}
