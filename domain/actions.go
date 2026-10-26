package domain

import "time"

// Runner scopes: which repositories a runner takes jobs from.
const (
	RunnerScopeSite       = "site"       // every repository (site admins)
	RunnerScopeGroup      = "group"      // the group's repositories (group owners)
	RunnerScopeRepository = "repository" // one repository (maintainers)
)

// Run statuses. A run is one workflow file triggered by one event; a runner
// takes it whole (act runs its jobs, needs and matrix included).
const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSuccess   = "success"
	RunFailure   = "failure"
	RunCancelled = "cancelled"
)

// RunDone reports whether status is final.
func RunDone(status string) bool {
	return status == RunSuccess || status == RunFailure || status == RunCancelled
}

// ActionRunner is a registered `ogit runner`.
type ActionRunner struct {
	Id         string `gorm:"primaryKey"`
	Name       string
	ScopeType  string
	ScopeId    *string // group or repository id; nil for site runners
	Labels     string  // comma-separated, reported by the runner
	TokenHash  string  `json:"-"`
	Version    string
	LastSeenAt *time.Time
	CreatedAt  time.Time
}

func (ActionRunner) TableName() string { return "action_runner" }

// ActionRegistrationToken registers one runner in a scope (used once,
// expires).
type ActionRegistrationToken struct {
	Id        string `gorm:"primaryKey"`
	ScopeType string
	ScopeId   *string
	TokenHash string
	CreatedBy string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (ActionRegistrationToken) TableName() string { return "action_registration_token" }

// ActionRun is one workflow run.
type ActionRun struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	Number       int
	WorkflowPath string // e.g. .github/workflows/ci.yml
	WorkflowName string
	Workflow     string `json:"-"` // the file's content at Sha
	Event        string
	EventPayload string `json:"-"`
	Ref          string
	Sha          string
	ActorId      *string
	ActorName    string
	Labels       string // runs-on labels a runner must have (comma-separated)
	Status       string
	RunnerId     *string
	TokenHash    string `json:"-"` // the job token, while the run is running
	CancelAsked  bool
	HeartbeatAt  *time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
	CreatedAt    time.Time
}

func (ActionRun) TableName() string { return "action_run" }

// ActionJob is a job of a run, as reported by its runner (matrix jobs are
// one each).
type ActionJob struct {
	Id         string `gorm:"primaryKey"`
	RunId      string
	Name       string
	Status     string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

func (ActionJob) TableName() string { return "action_job" }

// ActionLog is a chunk of a run's log, in order of Seq.
type ActionLog struct {
	RunId string `gorm:"primaryKey"`
	Seq   int    `gorm:"primaryKey"`
	Data  string
}

func (ActionLog) TableName() string { return "action_log" }

type ActionsPers interface {
	CreateRegistrationToken(t *ActionRegistrationToken) error
	// TakeRegistrationToken deletes and returns the unexpired token with
	// that hash (one use).
	TakeRegistrationToken(hash string, now time.Time) (*ActionRegistrationToken, error)

	CreateRunner(r *ActionRunner) error
	RunnerByTokenHash(hash string) (*ActionRunner, error)
	Runners(scopeType string, scopeId *string) ([]ActionRunner, error)
	GetRunner(id string) (*ActionRunner, error)
	DeleteRunner(id string) error
	TouchRunner(id, labels, version string, at time.Time) error

	// CreateRun numbers the run (repository.last_run_number) and inserts it.
	CreateRun(r *ActionRun) error
	GetRun(repositoryId string, number int) (*ActionRun, error)
	GetRunById(id string) (*ActionRun, error)
	ListRuns(repositoryId string, offset, limit int) ([]ActionRun, int64, error)
	// QueuedRuns lists queued runs, oldest first, in the given repositories
	// (nil: every repository).
	QueuedRuns(repositoryIds []string, limit int) ([]ActionRun, error)
	// ClaimRun moves a queued run to running for runnerId, if it is still
	// queued (false otherwise).
	ClaimRun(runId, runnerId, tokenHash string, now time.Time) (bool, error)
	UpdateRun(r *ActionRun, fields ...string) error
	RunByTokenHash(hash string) (*ActionRun, error)
	// StaleRuns lists running runs whose heartbeat is older than before.
	StaleRuns(before time.Time) ([]ActionRun, error)

	UpsertJob(j *ActionJob) error
	Jobs(runId string) ([]ActionJob, error)

	AppendLog(runId string, data string) error
	// Logs returns the chunks from seq on.
	Logs(runId string, fromSeq int) ([]ActionLog, error)
}
