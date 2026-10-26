package infrastructure

import (
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/cronscheduler"
	"github.com/labbs/ogit/infrastructure/database"
	"github.com/labbs/ogit/infrastructure/http"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

type Deps struct {
	Config        config.Config
	Logger        zerolog.Logger
	Http          http.Config
	CronScheduler cronscheduler.Config
	Database      database.Config
	Storage       storage.Config
	Mailer        mail.Sender

	UserApp         *application.UserApp
	SessionApp      *application.SessionApp
	AuthApp         *application.AuthApp
	RepoApp         *application.RepositoryApp
	BrowseApp       *application.BrowseApp
	PullApp         *application.PullRequestApp
	Limits          *ratelimit.Guard
	GitApp          *application.GitApp
	TokenApp        *application.TokenApp
	GroupApp        *application.GroupApp
	AdminApp        *application.AdminApp
	SSHKeyApp       *application.SSHKeyApp
	WebhookApp      *application.WebhookApp
	ProtectionApp   *application.ProtectionApp
	StatusApp       *application.StatusApp
	IssueApp        *application.IssueApp
	MilestoneApp    *application.MilestoneApp
	RepoTokenApp    *application.RepositoryTokenApp
	ReleaseApp      *application.ReleaseApp
	TwoFactorApp    *application.TwoFactorApp
	NotificationApp *application.NotificationApp
	LFSApp          *application.LFSApp
	ActionsApp      *application.ActionsApp
	PackagesApp     *application.PackagesApp
}
