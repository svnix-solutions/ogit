// Package storage is the `ogit storage` command.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/database"
	"github.com/labbs/ogit/infrastructure/logger"
	"github.com/labbs/ogit/infrastructure/persistence"
	gitstorage "github.com/labbs/ogit/infrastructure/storage"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/urfave/cli/v3"
)

func NewInstance(version string) *cli.Command {
	cfg := &config.Config{}
	cfg.Version = version
	var slug string
	var all bool

	flags := func() (list []cli.Flag) {
		list = append(list, config.GenericFlags(cfg)...)
		list = append(list, config.LoggerFlags(cfg)...)
		list = append(list, config.DatabaseFlags(cfg)...)
		list = append(list, config.StorageFlags(cfg)...)
		list = append(list,
			&cli.StringFlag{Name: "repository", Usage: "slug of the repository to repack", Destination: &slug},
			&cli.BoolFlag{Name: "all", Usage: "repack every repository", Destination: &all},
		)
		return
	}

	return &cli.Command{
		Name:  "storage",
		Usage: "Storage maintenance",
		Commands: []*cli.Command{{
			Name: "repack",
			Usage: "Convert S3 repositories stored as loose objects (before packs were introduced) into one pack each. " +
				"Safe to run while the server is up; loose objects are deleted only after the pack is verified.",
			Flags: flags(),
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return repack(*cfg, slug, all, cmd.Root().Writer)
			},
		}},
	}
}

func repack(cfg config.Config, slug string, all bool, out io.Writer) error {
	if (slug == "") == !all {
		return errors.New("specify --repository <slug> or --all")
	}

	l := logger.NewLogger(cfg.Logger.Level, cfg.Logger.Pretty, cfg.Version)
	store, err := gitstorage.Configure(cfg, l)
	if err != nil {
		return err
	}
	s3store, ok := store.(*s3.S3Storage)
	if !ok {
		_, _ = fmt.Fprintf(out, "storage type %q already stores packs; nothing to do\n", cfg.Storage.Type)
		return nil
	}

	db, err := database.Configure(cfg, l)
	if err != nil {
		return err
	}
	repos := persistence.NewRepositoryPers(db.Db)

	var targets []domain.Repository
	if all {
		for offset := 0; ; offset += 200 {
			page, _, err := repos.List(200, offset)
			if err != nil {
				return err
			}
			targets = append(targets, page...)
			if len(page) < 200 {
				break
			}
		}
	} else {
		repo, err := repos.GetRepository(domain.Repository{Slug: slug})
		if err != nil {
			return fmt.Errorf("repository %q not found: %w", slug, err)
		}
		targets = append(targets, *repo)
	}

	for _, repo := range targets {
		n, err := s3store.Repack(repo.StorageKey)
		if err != nil {
			return fmt.Errorf("repack %s: %w", repo.Slug, err)
		}
		if n == 0 {
			_, _ = fmt.Fprintf(out, "%s: already packed\n", repo.Slug)
		} else {
			_, _ = fmt.Fprintf(out, "%s: packed %d loose objects\n", repo.Slug, n)
		}
	}
	return nil
}
