// Package storage is the `ogit storage` command.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/labbs/ogit/application"
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
	var grace, expire time.Duration
	var dryRun bool
	graceFlag := func() cli.Flag {
		return &cli.DurationFlag{
			Name:        "grace",
			Usage:       "how long replaced packs stay readable for requests that started before the rewrite",
			Value:       time.Hour,
			Destination: &grace,
		}
	}

	flags := func() (list []cli.Flag) {
		list = append(list, config.GenericFlags(cfg)...)
		list = append(list, config.LoggerFlags(cfg)...)
		list = append(list, config.DatabaseFlags(cfg)...)
		list = append(list, config.StorageFlags(cfg)...)
		list = append(list,
			&cli.StringFlag{Name: "repository", Usage: "the repository, as <owner>/<name>", Destination: &slug},
			&cli.BoolFlag{Name: "all", Usage: "every repository", Destination: &all},
		)
		return
	}

	return &cli.Command{
		Name:  "storage",
		Usage: "Storage maintenance",
		Commands: []*cli.Command{{
			Name: "repack",
			Usage: "Convert S3 repositories stored as loose objects (before packs were introduced) into one pack each. " +
				"Loose objects are deleted only after the pack is verified.",
			Flags: flags(),
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return repack(*cfg, slug, all, cmd.Root().Writer)
			},
		}, {
			Name: "gc",
			Usage: "Merge the small packs of S3 repositories (each push adds one), and delete the packs replaced " +
				"by an earlier run more than --grace ago. Objects are never pruned. Safe to run while the server is up.",
			Flags: append(flags(), graceFlag()),
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return gc(*cfg, slug, all, grace, cmd.Root().Writer)
			},
		}, {
			Name: "prune",
			Usage: "Delete objects of S3 repositories that no ref reaches any more (force-pushes, deleted branches), " +
				"once they are older than --expire. Rewrites the old packs: run it rarely (e.g. weekly). " +
				"Safe to run while the server is up.",
			Flags: append(flags(), graceFlag(),
				&cli.DurationFlag{
					Name:        "expire",
					Usage:       "only prune objects older than this, so force-pushed work can still be recovered by its hash",
					Value:       14 * 24 * time.Hour,
					Destination: &expire,
				},
				&cli.BoolFlag{Name: "dry-run", Usage: "report what would be pruned, change nothing", Destination: &dryRun},
			),
			Action: func(ctx context.Context, cmd *cli.Command) error {
				return prune(*cfg, slug, all, expire, grace, dryRun, cmd.Root().Writer)
			},
		}},
	}
}

// s3Targets returns the S3 storage and the repositories selected by
// --repository or --all; a nil storage means another storage type.
func s3Targets(cfg config.Config, slug string, all bool) (*s3.S3Storage, []domain.Repository, error) {
	if (slug == "") == !all {
		return nil, nil, errors.New("specify --repository <owner>/<name> or --all")
	}

	l := logger.NewLogger(cfg.Logger.Level, cfg.Logger.Pretty, cfg.Version)
	store, err := gitstorage.Configure(cfg, l)
	if err != nil {
		return nil, nil, err
	}
	s3store, ok := store.(*s3.S3Storage)
	if !ok {
		return nil, nil, nil
	}

	db, err := database.Configure(cfg, l)
	if err != nil {
		return nil, nil, err
	}
	repos := persistence.NewRepositoryPers(db.Db)

	var targets []domain.Repository
	if all {
		for offset := 0; ; offset += 200 {
			page, _, err := repos.List(200, offset)
			if err != nil {
				return nil, nil, err
			}
			targets = append(targets, page...)
			if len(page) < 200 {
				break
			}
		}
	} else {
		namespace, name, ok := application.SplitPath(slug)
		if !ok {
			return nil, nil, fmt.Errorf("--repository takes <owner>/<name>, not %q", slug)
		}
		repo, err := repos.GetByPath(namespace, name)
		if err != nil {
			return nil, nil, fmt.Errorf("repository %q not found: %w", slug, err)
		}
		targets = append(targets, *repo)
	}
	// Wikis are repositories of their own (<key>.wiki).
	for _, repo := range targets {
		if wiki := domain.WikiOf(&repo); s3store.RepositoryExists(wiki.StorageKey) {
			targets = append(targets, *wiki)
		}
	}
	return s3store, targets, nil
}

func repack(cfg config.Config, slug string, all bool, out io.Writer) error {
	s3store, targets, err := s3Targets(cfg, slug, all)
	if err != nil {
		return err
	}
	if s3store == nil {
		_, _ = fmt.Fprintf(out, "storage type %q already stores packs; nothing to do\n", cfg.Storage.Type)
		return nil
	}

	for _, repo := range targets {
		n, err := s3store.Repack(repo.StorageKey)
		if err != nil {
			return fmt.Errorf("repack %s: %w", repo.GitPath(), err)
		}
		if n == 0 {
			_, _ = fmt.Fprintf(out, "%s: already packed\n", repo.GitPath())
		} else {
			_, _ = fmt.Fprintf(out, "%s: packed %d loose objects\n", repo.GitPath(), n)
		}
	}
	return nil
}

func gc(cfg config.Config, slug string, all bool, grace time.Duration, out io.Writer) error {
	if grace < 0 {
		return errors.New("--grace must not be negative")
	}
	s3store, targets, err := s3Targets(cfg, slug, all)
	if err != nil {
		return err
	}
	if s3store == nil {
		_, _ = fmt.Fprintf(out, "storage type %q: use git gc on the repositories; nothing to do\n", cfg.Storage.Type)
		return nil
	}

	var failed []string
	for _, repo := range targets {
		res, err := s3store.GC(repo.StorageKey, grace)
		if err != nil {
			// Keep going: one broken repository must not block the others.
			_, _ = fmt.Fprintf(out, "%s: %v\n", repo.GitPath(), err)
			failed = append(failed, repo.GitPath())
			continue
		}
		var done []string
		if res.Loose > 0 {
			done = append(done, fmt.Sprintf("packed %d loose objects", res.Loose))
		}
		if res.Merged > 0 {
			done = append(done, fmt.Sprintf("merged %d packs (%d objects)", res.Merged, res.Objects))
		}
		if res.Deleted > 0 {
			done = append(done, fmt.Sprintf("deleted %d replaced packs", res.Deleted))
		}
		if len(done) == 0 {
			done = append(done, "nothing to do")
		}
		_, _ = fmt.Fprintf(out, "%s: %s\n", repo.GitPath(), strings.Join(done, ", "))
	}
	if len(failed) > 0 {
		return fmt.Errorf("gc failed for %s", strings.Join(failed, ", "))
	}
	return nil
}

func prune(cfg config.Config, slug string, all bool, expire, grace time.Duration, dryRun bool, out io.Writer) error {
	if grace < 0 || expire < 0 {
		return errors.New("--grace and --expire must not be negative")
	}
	s3store, targets, err := s3Targets(cfg, slug, all)
	if err != nil {
		return err
	}
	if s3store == nil {
		_, _ = fmt.Fprintf(out, "storage type %q: use git gc --prune on the repositories; nothing to do\n", cfg.Storage.Type)
		return nil
	}

	var failed []string
	for _, repo := range targets {
		res, err := s3store.Prune(repo.StorageKey, expire, grace, dryRun)
		if err != nil {
			_, _ = fmt.Fprintf(out, "%s: %v\n", repo.GitPath(), err)
			failed = append(failed, repo.GitPath())
			continue
		}
		var done []string
		if res.Loose > 0 {
			done = append(done, fmt.Sprintf("packed %d loose objects", res.Loose))
		}
		switch {
		case res.Pruned > 0 && dryRun:
			done = append(done, fmt.Sprintf("would prune %d unreachable objects (from %d old packs, %d objects kept)", res.Pruned, res.Packs, res.Kept))
		case res.Pruned > 0:
			done = append(done, fmt.Sprintf("pruned %d unreachable objects (from %d old packs, %d objects kept)", res.Pruned, res.Packs, res.Kept))
		}
		if res.Deleted > 0 {
			done = append(done, fmt.Sprintf("deleted %d replaced packs", res.Deleted))
		}
		if len(done) == 0 {
			done = append(done, "nothing to prune")
		}
		_, _ = fmt.Fprintf(out, "%s: %s\n", repo.GitPath(), strings.Join(done, ", "))
	}
	if len(failed) > 0 {
		return fmt.Errorf("prune failed for %s", strings.Join(failed, ", "))
	}
	return nil
}
