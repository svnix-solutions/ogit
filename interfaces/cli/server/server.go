package server

import (
	"context"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"net"
	"strconv"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/cronscheduler"
	"github.com/labbs/ogit/infrastructure/database"
	"github.com/labbs/ogit/infrastructure/http"
	"github.com/labbs/ogit/infrastructure/jobs"
	"github.com/labbs/ogit/infrastructure/logger"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/labbs/ogit/infrastructure/persistence"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/labbs/ogit/infrastructure/webhook"
	routes "github.com/labbs/ogit/interfaces/http"
	sshserver "github.com/labbs/ogit/interfaces/ssh"

	"github.com/urfave/cli/v3"
)

// NewInstance creates a new CLI command for starting the server.
// It's called by the main application to add the "server" command to the CLI.
func NewInstance(version string) *cli.Command {
	cfg := &config.Config{}
	cfg.Version = version
	serverFlags := getFlags(cfg)

	return &cli.Command{
		Name:  "server",
		Usage: "Start the HTTP server",
		Flags: serverFlags,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runServer(*cfg)
		},
	}
}

// getFlags returns the list of CLI flags required for the server command.
func getFlags(cfg *config.Config) (list []cli.Flag) {
	list = append(list, config.GenericFlags(cfg)...)
	list = append(list, config.ServerFlags(cfg)...)
	list = append(list, config.LoggerFlags(cfg)...)
	list = append(list, config.DatabaseFlags(cfg)...)
	list = append(list, config.SessionFlags(cfg)...)
	list = append(list, config.RegistrationFlags(cfg)...)
	list = append(list, config.StorageFlags(cfg)...)
	list = append(list, config.SMTPFlags(cfg)...)
	list = append(list, config.SSHFlags(cfg)...)
	list = append(list, config.RateLimitFlags(cfg)...)
	list = append(list, config.WebhookFlags(cfg)...)
	list = append(list, config.ReleaseFlags(cfg)...)
	return
}

// runServer initializes the necessary dependencies and starts the HTTP server.
func runServer(cfg config.Config) error {
	deps, err := setupDeps(cfg)
	if err != nil {
		return err
	}
	logger := deps.Logger.With().Str("component", "interfaces.cli.http.runserver").Logger()

	// Setup cron jobs
	configJobs := jobs.Config{
		Logger:        deps.Logger,
		CronScheduler: deps.CronScheduler,
		SessionApp:    *deps.SessionApp,
	}

	err = configJobs.SetupJobs()
	if err != nil {
		logger.Fatal().Err(err).Str("event", "http.runserver.jobs.setup").Msg("Failed to setup cron jobs")
		return err
	}

	// Setup routes
	routes.SetupRoutes(deps)

	// Webhook deliveries, on every server (each delivery is claimed once).
	go deps.WebhookApp.Run(context.Background(), 2*time.Second)

	if cfg.SSH.Enabled {
		if err := startSSH(deps); err != nil {
			logger.Fatal().Err(err).Str("event", "http.runserver.ssh").Msg("Failed to start SSH server")
			return err
		}
	}

	// Start HTTP server
	logger.Info().Str("event", "http.runserver.http.listen").Msgf("Starting HTTP server on port %d", cfg.Server.Port)
	err = deps.Http.Fiber.Listen(":" + strconv.Itoa(cfg.Server.Port))
	if err != nil {
		logger.Fatal().Err(err).Str("event", "http.runserver.http.listen").Msg("Failed to start HTTP server")
		return err
	}

	return nil
}

// setupDeps initializes the logger, cron scheduler, database, storage,
// application services and HTTP server. Routes and cron jobs are not set up.
func setupDeps(cfg config.Config) (infrastructure.Deps, error) {
	var err error
	var deps infrastructure.Deps

	// Initialize logger
	deps.Logger = logger.NewLogger(cfg.Logger.Level, cfg.Logger.Pretty, cfg.Version)
	logger := deps.Logger.With().Str("component", "interfaces.cli.http.runserver").Logger()

	// Validate configuration before anything uses it
	if err = cfg.EnsureSessionSecret(deps.Logger); err != nil {
		logger.Error().Err(err).Str("event", "http.runserver.config.session_secret").Msg("Invalid session configuration")
		return deps, err
	}
	if err = cfg.ValidateEmailVerification(); err != nil {
		logger.Error().Err(err).Str("event", "http.runserver.config.email_verification").Msg("Invalid email verification configuration")
		return deps, err
	}
	deps.Config = cfg

	// Initialize other cron scheduler (go-cron)
	deps.CronScheduler, err = cronscheduler.Configure(deps.Logger)
	if err != nil {
		logger.Fatal().Err(err).Str("event", "http.runserver.cronscheduler.configure").Msg("Failed to configure cron scheduler")
		return deps, err
	}

	// Initialize database connection (gorm)
	deps.Database, err = database.Configure(deps.Config, deps.Logger)
	if err != nil {
		logger.Fatal().Err(err).Str("event", "http.runserver.database.configure").Msg("Failed to configure database connection")
		return deps, err
	}

	// Initialize storage
	deps.Storage, err = storage.Configure(deps.Config, deps.Logger)
	if err != nil {
		logger.Fatal().Err(err).Str("event", "http.runserver.storage.configure").Msg("Failed to configure storage")
		return deps, err
	}

	// Initialize application services
	userPers := persistence.NewUserPers(deps.Database.Db)
	groupPers := persistence.NewGroupPers(deps.Database.Db)
	sessionPers := persistence.NewSessionPers(deps.Database.Db)
	repoPers := persistence.NewRepositoryPers(deps.Database.Db)
	tokenPers := persistence.NewTokenPers(deps.Database.Db)
	repositoryMemberPers := persistence.NewRepositoryMemberPers(deps.Database.Db)

	// Before the services that send mail (SSHKeyApp got nil before).
	deps.Mailer = mail.NewSMTPSender(deps.Config)

	deps.UserApp = application.NewUserApp(deps.Config, deps.Logger, userPers, groupPers)
	deps.SessionApp = application.NewSessionApp(deps.Config, deps.Logger, sessionPers, deps.UserApp)
	deps.RepoApp = application.NewRepositoryApp(deps.Config, deps.Logger, repoPers, repositoryMemberPers, groupPers, userPers, nil, deps.Storage)
	deps.BrowseApp = &application.BrowseApp{Repositories: deps.RepoApp, Storage: deps.Storage}
	deps.PullApp = &application.PullRequestApp{
		Config: deps.Config, Logger: deps.Logger, Repositories: deps.RepoApp, Users: userPers, Mailer: deps.Mailer,
		Pulls: persistence.NewPullRequestPers(deps.Database.Db), Reviews: persistence.NewReviewPers(deps.Database.Db),
		Labels: persistence.NewLabelPers(deps.Database.Db),
	}
	sender, err := webhook.NewSender(deps.Config.Webhooks.AllowedHosts, deps.Config.Webhooks.Timeout)
	if err != nil {
		logger.Error().Err(err).Str("event", "http.runserver.config.webhooks").Msg("Invalid webhooks.allowed_hosts")
		return deps, err
	}
	deps.WebhookApp = &application.WebhookApp{
		Config: deps.Config, Logger: deps.Logger, Repositories: deps.RepoApp, Users: userPers,
		Hooks: persistence.NewWebhookPers(deps.Database.Db), Sender: sender,
		Pulls: deps.PullApp.Pulls, Labels: deps.PullApp.Labels,
	}
	deps.RepoApp.Webhooks = deps.WebhookApp
	deps.PullApp.Webhooks = deps.WebhookApp
	deps.ProtectionApp = &application.ProtectionApp{
		Logger: deps.Logger, Repositories: deps.RepoApp, Rules: persistence.NewBranchProtectionPers(deps.Database.Db),
		Tags: persistence.NewProtectedTagPers(deps.Database.Db), Users: userPers, Groups: groupPers,
	}
	deps.PullApp.Protection = deps.ProtectionApp
	deps.StatusApp = &application.StatusApp{
		Logger: deps.Logger, Repositories: deps.RepoApp, Statuses: persistence.NewCommitStatusPers(deps.Database.Db), Webhooks: deps.WebhookApp,
	}
	deps.PullApp.Statuses = deps.StatusApp
	assignees, milestones := persistence.NewAssigneePers(deps.Database.Db), persistence.NewMilestonePers(deps.Database.Db)
	deps.WebhookApp.Assignees, deps.WebhookApp.Milestones = assignees, milestones
	deps.IssueApp = &application.IssueApp{
		Config: deps.Config, Logger: deps.Logger, Repositories: deps.RepoApp, Issues: persistence.NewIssuePers(deps.Database.Db),
		Pulls: deps.PullApp.Pulls, Labels: deps.PullApp.Labels, Assignees: assignees, Milestones: milestones,
		Webhooks: deps.WebhookApp, Mailer: deps.Mailer,
	}
	deps.PullApp.Assignees, deps.PullApp.Milestones, deps.PullApp.Issues = assignees, milestones, deps.IssueApp
	deps.MilestoneApp = &application.MilestoneApp{Logger: deps.Logger, Repositories: deps.RepoApp, Milestones: milestones, Webhooks: deps.WebhookApp}
	deps.TokenApp = application.NewTokenApp(deps.Config, deps.Logger, tokenPers)
	deps.GroupApp = application.NewGroupApp(deps.Config, deps.Logger, groupPers, userPers, repoPers)
	deps.AdminApp = &application.AdminApp{
		Config: deps.Config, Logger: deps.Logger, UserPers: userPers, SessionPers: sessionPers,
		RepoPers: repoPers, RepositoryMemberPers: repositoryMemberPers, GroupPers: groupPers, Storage: deps.Storage,
	}
	deps.SSHKeyApp = &application.SSHKeyApp{
		Config: deps.Config, Logger: deps.Logger, SSHKeyPers: persistence.NewSSHKeyPers(deps.Database.Db), UserPers: userPers,
		RepositoryApp: deps.RepoApp, AdminApp: deps.AdminApp, Mailer: deps.Mailer,
	}
	deps.AuthApp = application.NewAuthApp(deps.Config, deps.Logger, *deps.UserApp, *deps.SessionApp, *deps.TokenApp, *deps.RepoApp,
		deps.Mailer, persistence.NewEmailVerificationPers(deps.Database.Db))
	deps.AuthApp.ForkEdits = deps.PullApp.EditableBranches
	deps.RepoTokenApp = &application.RepositoryTokenApp{Repositories: deps.RepoApp, Tokens: deps.TokenApp}
	deps.ReleaseApp = &application.ReleaseApp{
		Logger: deps.Logger, Repositories: deps.RepoApp, Releases: persistence.NewReleasePers(deps.Database.Db),
		Assets: persistence.NewReleaseAssetPers(deps.Database.Db), MaxAssetSize: deps.Config.Releases.MaxAssetSize,
		Pulls: deps.PullApp.Pulls, Users: userPers, Webhooks: deps.WebhookApp, TagCheck: deps.ProtectionApp.ReleaseTagCheck,
	}

	deps.Limits, err = ratelimit.NewGuard(deps.Config, deps.Logger)
	if err != nil {
		logger.Error().Err(err).Str("event", "http.runserver.config.ratelimit").Msg("Invalid rate limit configuration")
		return deps, err
	}

	// Initialize HTTP server (fiber + fiberoapi)
	deps.Http, err = http.Configure(deps.Config, deps.Logger, *deps.SessionApp, true)
	if err != nil {
		logger.Fatal().Err(err).Str("event", "http.runserver.http.configure").Msg("Failed to configure HTTP server")
		return deps, err
	}

	return deps, nil
}

// startSSH listens for git over SSH on ssh.port in the background.
func startSSH(deps infrastructure.Deps) error {
	srv, err := sshserver.New(deps.Config.SSH.HostKeyPath, deps.Logger, deps.AuthApp, deps.SSHKeyApp.SSHKeyPers, deps.Storage)
	if srv != nil {
		srv.Limits = deps.Limits
		srv.Webhooks = deps.WebhookApp
		srv.Protection = deps.ProtectionApp
		srv.Pulls = deps.PullApp
	}
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(deps.Config.SSH.Port))
	if err != nil {
		return err
	}
	deps.Logger.Info().Str("event", "http.runserver.ssh.listen").Msgf("Starting SSH server on port %d", deps.Config.SSH.Port)
	go func() {
		if err := srv.Serve(ln); err != nil {
			deps.Logger.Error().Err(err).Msg("SSH server stopped")
		}
	}()
	return nil
}
