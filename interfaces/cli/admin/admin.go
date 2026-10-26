// Package admin is the `ogit admin` command: promote or demote site admins
// directly in the database. It is how the first admin is created, and the
// recovery path when no admin is left (the API refuses to remove the last).
// reset-2fa turns off a user's two-factor authentication, for when no admin
// can sign in to do it through the API.
package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/database"
	"github.com/labbs/ogit/infrastructure/logger"
	"github.com/labbs/ogit/infrastructure/persistence"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v3"
)

func NewInstance(version string) *cli.Command {
	cfg := &config.Config{}
	cfg.Version = version
	var email, username string

	flags := func() (list []cli.Flag) {
		list = append(list, config.GenericFlags(cfg)...)
		list = append(list, config.LoggerFlags(cfg)...)
		list = append(list, config.DatabaseFlags(cfg)...)
		list = append(list,
			&cli.StringFlag{Name: "email", Usage: "the user's email", Destination: &email},
			&cli.StringFlag{Name: "username", Usage: "the user's username", Destination: &username},
		)
		return
	}

	setRole := func(role domain.Role) cli.ActionFunc {
		return func(ctx context.Context, cmd *cli.Command) error {
			return run(*cfg, email, username, role, cmd.Root().Writer)
		}
	}

	return &cli.Command{
		Name:  "admin",
		Usage: "Manage site administrators (uses the database directly)",
		Commands: []*cli.Command{
			{Name: "promote", Usage: "Make a user a site admin", Flags: flags(), Action: setRole(domain.RoleAdmin)},
			{Name: "demote", Usage: "Make a site admin a regular user", Flags: flags(), Action: setRole(domain.RoleUser)},
			{Name: "reset-2fa", Usage: "Turn off a user's two-factor authentication (lost device and recovery codes)", Flags: flags(),
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return resetTwoFactor(*cfg, email, username, cmd.Root().Writer)
				}},
		},
	}
}

type writer = interface{ Write([]byte) (int, error) }

// find opens the database and finds the user by email or username.
func find(cfg config.Config, email, username string) (*database.Config, *domain.User, zerolog.Logger, error) {
	l := logger.NewLogger(cfg.Logger.Level, cfg.Logger.Pretty, cfg.Version)
	if (email == "") == (username == "") {
		return nil, nil, l, errors.New("specify exactly one of --email or --username")
	}
	db, err := database.Configure(cfg, l)
	if err != nil {
		return nil, nil, l, err
	}
	users := persistence.NewUserPers(db.Db)
	var user domain.User
	if email != "" {
		user, err = users.GetByEmail(email)
	} else {
		user, err = users.GetByUsername(username)
	}
	if err != nil {
		return nil, nil, l, fmt.Errorf("user not found: %w", err)
	}
	return &db, &user, l, nil
}

func resetTwoFactor(cfg config.Config, email, username string, out writer) error {
	db, user, l, err := find(cfg, email, username)
	if err != nil {
		return err
	}
	if err := persistence.NewTwoFactorPers(db.Db).Disable(user.Id); err != nil {
		return err
	}
	l.Info().Str("component", "audit").Str("actor", "cli").Str("action", "user.reset_2fa").Str("user_id", user.Id).Send()
	_, _ = fmt.Fprintf(out, "two-factor authentication is off for %s (%s)\n", user.Username, user.Email)
	return nil
}

func run(cfg config.Config, email, username string, role domain.Role, out writer) error {
	db, user, l, err := find(cfg, email, username)
	if err != nil {
		return err
	}
	users := persistence.NewUserPers(db.Db)

	if err := users.SetRole(user.Id, role); err != nil {
		return err
	}
	l.Info().Str("component", "audit").Str("actor", "cli").Str("action", "user.role").
		Str("user_id", user.Id).Str("role", string(role)).Send()
	_, _ = fmt.Fprintf(out, "%s (%s) is now %s\n", user.Username, user.Email, role)
	return nil
}
