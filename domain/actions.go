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
	Id string `gorm:"primaryKey"`
	// ApiId is the run's number for GitHub's API and artifact clients
	// (GITHUB_RUN_ID), random below 2^53.
	ApiId        int64
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
	// NeedsApproval: a first-time contributor's fork pull request; the run
	// waits (queued) until a maintainer approves it.
	NeedsApproval bool
	// FromFork: no secrets, and a read-only token.
	FromFork bool
	// Permissions of the job token, "contents:write,statuses:write"
	// (read when absent).
	Permissions string
	// StatusSha is the commit job statuses are posted on (a pull request's
	// head, not its merge commit); "" means Sha.
	StatusSha   string
	PullNumber  int
	HeartbeatAt *time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
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

// ActionSecret is an encrypted secret; ActionVariable a plain variable.
// ScopeId is "" for the site.
type ActionSecret struct {
	Id        string `gorm:"primaryKey"`
	ScopeType string
	ScopeId   string
	Name      string
	Value     string `json:"-"` // sealed
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (ActionSecret) TableName() string { return "action_secret" }

type ActionVariable struct {
	Id        string `gorm:"primaryKey"`
	ScopeType string
	ScopeId   string
	Name      string
	Value     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (ActionVariable) TableName() string { return "action_variable" }

// ActionSchedule is a `schedule:` cron of a workflow on the default branch.
type ActionSchedule struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	WorkflowPath string
	Cron         string
	NextRunAt    time.Time
}

func (ActionSchedule) TableName() string { return "action_schedule" }

// ActionArtifact is an artifact uploaded by a run (actions/upload-artifact
// v4), stored in the repository's "artifacts" area under its id. Ready
// once its upload is finalized.
type ActionArtifact struct {
	Id           string `gorm:"primaryKey"`
	ApiId        int64
	RunId        string
	RepositoryId string
	Name         string
	Size         int64
	Ready        bool
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

func (ActionArtifact) TableName() string { return "action_artifact" }

// ActionArtifactBlock is a stored block of an upload in progress.
type ActionArtifactBlock struct {
	ArtifactId string `gorm:"primaryKey"`
	BlockId    string `gorm:"primaryKey"`
}

func (ActionArtifactBlock) TableName() string { return "action_artifact_block" }

// ActionCache is an actions/cache entry: a key and version saved by a run
// of Scope (its ref), stored in the repository's "actions-cache" area
// under its id; Complete once committed.
type ActionCache struct {
	Id           string `gorm:"primaryKey"`
	ApiId        int64
	RepositoryId string
	Scope        string
	CacheKey     string
	Version      string
	Size         int64
	Complete     bool
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

func (ActionCache) TableName() string { return "action_cache" }

// ActionCacheChunk is an uploaded chunk of an entry, at Start.
type ActionCacheChunk struct {
	CacheId string `gorm:"primaryKey"`
	Start   int64  `gorm:"primaryKey"`
	Size    int64
}

func (ActionCacheChunk) TableName() string { return "action_cache_chunk" }

// RunFilter narrows a run list; zero values match everything. Status
// "waiting" is a queued run that needs approval.
type RunFilter struct {
	WorkflowPath string
	Ref          string
	Event        string
	Status       string
	PullNumber   int
}

type ActionsPers interface {
	// Cache.
	CreateCache(c *ActionCache) error
	CacheByApiId(repositoryId string, apiId int64) (*ActionCache, error)
	CacheById(id string) (*ActionCache, error)
	// CacheEntry is the complete entry of a scope with that key and version
	// (prefix: the newest whose key starts with key).
	CacheEntry(repositoryId, scope, key, version string, prefix bool) (*ActionCache, error)
	CacheExisting(repositoryId, scope, key, version string) (*ActionCache, error)
	Caches(repositoryId string) ([]ActionCache, error)
	CompleteCache(id string, size int64) error
	TouchCache(id string, at time.Time) error
	DeleteCache(id string) error
	AddCacheChunk(ch *ActionCacheChunk) error
	CacheChunks(cacheId string) ([]ActionCacheChunk, error)
	// StaleCaches lists entries unused since before, and uploads started
	// before incompleteBefore and never committed.
	StaleCaches(before, incompleteBefore time.Time, limit int) ([]ActionCache, error)
	// CacheRepositoriesOver lists repositories whose complete entries
	// weigh more than limit bytes.
	CacheRepositoriesOver(limit int64) ([]string, error)

	// Artifacts.
	CreateArtifact(a *ActionArtifact) error
	Artifact(runId, name string) (*ActionArtifact, error)
	ArtifactByApiId(repositoryId string, apiId int64) (*ActionArtifact, error)
	// Artifacts lists a run's ready artifacts, by name.
	Artifacts(runId string) ([]ActionArtifact, error)
	ArtifactById(id string) (*ActionArtifact, error)
	SetArtifactSize(id string, size int64) error
	// AddArtifactBlock records a stored block; ArtifactBlocks lists them
	// and ForgetArtifactBlocks drops the records.
	AddArtifactBlock(artifactId, blockId string) error
	ArtifactBlocks(artifactId string) ([]string, error)
	ForgetArtifactBlocks(artifactId string) error
	MarkArtifactReady(id string, size int64) error
	DeleteArtifact(id string) error
	// ExpiredArtifacts lists expired artifacts and uploads unfinished for a
	// day.
	ExpiredArtifacts(now time.Time, limit int) ([]ActionArtifact, error)
	RunByApiId(repositoryId string, apiId int64) (*ActionRun, error)

	// Secrets and variables of a scope (scopeId "" for the site).
	Secrets(scopeType, scopeId string) ([]ActionSecret, error)
	PutSecret(s *ActionSecret) error
	DeleteSecret(scopeType, scopeId, name string) (bool, error)
	Variables(scopeType, scopeId string) ([]ActionVariable, error)
	PutVariable(v *ActionVariable) error
	DeleteVariable(scopeType, scopeId, name string) (bool, error)

	// ReplaceSchedules sets a repository's schedules.
	ReplaceSchedules(repositoryId string, schedules []ActionSchedule) error
	DueSchedules(now time.Time) ([]ActionSchedule, error)
	// ClaimSchedule moves a due schedule to next, if it is still at prev.
	ClaimSchedule(id string, prev, next time.Time) (bool, error)
	// HasMergedPull reports whether userId authored a merged pull request
	// in the repository.
	HasMergedPull(repositoryId, userId string) (bool, error)

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
	ListRuns(repositoryId string, f RunFilter, offset, limit int) ([]ActionRun, int64, error)
	// LatestRun is the newest run of a workflow on ref with a final status.
	LatestRun(repositoryId, workflowPath, ref string) (*ActionRun, error)
	// QueuedRuns lists queued runs that don't wait for approval, oldest
	// first, in the given repositories
	// (nil: every repository).
	QueuedRuns(repositoryIds []string, limit int) ([]ActionRun, error)
	// ClaimRun moves a queued run to running for runnerId, if it is still
	// queued (false otherwise).
	ClaimRun(runId, runnerId, tokenHash string, now time.Time) (bool, error)
	UpdateRun(r *ActionRun, fields ...string) error
	RunByTokenHash(hash string) (*ActionRun, error)
	// RequeueUnacknowledged puts back in the queue the runs claimed before
	// before whose runner never reported (heartbeat still at the claim).
	RequeueUnacknowledged(before time.Time) (int64, error)
	// StaleRuns lists running runs whose heartbeat is older than before.
	StaleRuns(before time.Time) ([]ActionRun, error)

	UpsertJob(j *ActionJob) error
	Jobs(runId string) ([]ActionJob, error)

	AppendLog(runId string, data string) error
	// Logs returns the chunks from seq on.
	Logs(runId string, fromSeq int) ([]ActionLog, error)
}
