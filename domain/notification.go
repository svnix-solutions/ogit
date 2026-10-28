package domain

import "time"

// Thread types: what a notification is about.
const (
	ThreadIssue       = "issue"
	ThreadPullRequest = "pull_request"
	ThreadRelease     = "release"
	ThreadWiki        = "wiki" // one thread per wiki (its id is the repository's)
)

// Reasons, as GitHub names them.
const (
	ReasonMention         = "mention"
	ReasonReviewRequested = "review_requested"
	ReasonAssign          = "assign"
	ReasonAuthor          = "author"
	ReasonComment         = "comment"     // took part in the thread
	ReasonCIActivity      = "ci_activity" // a check failed on your pull request
	ReasonSubscribed      = "subscribed"  // watching the repository
)

// Notification is one thread in a user's inbox, updated by every new event
// on it: unread again, with the latest reason and summary.
type Notification struct {
	Id            string `gorm:"primaryKey"`
	UserId        string
	RepositoryId  string
	Repository    *Repository `gorm:"foreignKey:RepositoryId"`
	ThreadType    string
	ThreadId      string
	ThreadNumber  int
	Title         string
	URL           string // the thread's web path, e.g. /owner/repo/issues/3
	Reason        string
	Participating bool // false: only because the user watches the repository
	Summary       string
	ActorId       *string
	Actor         *User `gorm:"foreignKey:ActorId"`
	Unread        bool
	UpdatedAt     time.Time
	CreatedAt     time.Time
}

func (Notification) TableName() string { return "notification" }

// Watch levels of a repository.
type WatchLevel string

const (
	WatchParticipating WatchLevel = "participating" // the default: only threads you take part in
	WatchAll           WatchLevel = "all"           // also new issues, pull requests and releases
	WatchReleases      WatchLevel = "releases"      // also releases
	WatchIgnore        WatchLevel = "ignore"        // nothing, not even mentions
)

func (l WatchLevel) Valid() bool {
	switch l {
	case WatchParticipating, WatchAll, WatchReleases, WatchIgnore:
		return true
	}
	return false
}

type RepositoryWatch struct {
	UserId       string `gorm:"primaryKey"`
	RepositoryId string `gorm:"primaryKey"`
	Level        WatchLevel
	UpdatedAt    time.Time
}

func (RepositoryWatch) TableName() string { return "repository_watch" }

// ThreadMute: the user unsubscribed from a thread.
type ThreadMute struct {
	UserId     string `gorm:"primaryKey"`
	ThreadType string `gorm:"primaryKey"`
	ThreadId   string `gorm:"primaryKey"`
	CreatedAt  time.Time
}

func (ThreadMute) TableName() string { return "thread_mute" }

// Email preferences for notifications.
type EmailLevel string

const (
	EmailParticipating EmailLevel = "participating" // the default
	EmailAll           EmailLevel = "all"           // also watched activity
	EmailNone          EmailLevel = "none"
)

type NotificationPreference struct {
	UserId string `gorm:"primaryKey"`
	Email  EmailLevel
}

func (NotificationPreference) TableName() string { return "notification_preference" }

// NotificationFilter selects inbox threads.
type NotificationFilter struct {
	All           bool // read ones too
	Participating bool // only threads the user takes part in
	RepositoryId  string
}

type NotificationPers interface {
	// Upsert records an event on a thread for a user: a new unread row, or
	// the existing one made unread with the new reason, summary and actor.
	Upsert(n *Notification) error
	List(userId string, f NotificationFilter, offset, limit int) ([]Notification, int64, error)
	UnreadCount(userId string) (int64, error)
	Get(userId, id string) (*Notification, error)
	SetUnread(userId, id string, unread bool) error
	// MarkAllRead marks the user's notifications read (in one repository
	// when repositoryId is set), up to before.
	MarkAllRead(userId, repositoryId string, before time.Time) error
	DeleteThread(threadType, threadId string) error

	Watch(userId, repositoryId string) (WatchLevel, error) // participating when unset
	SetWatch(userId, repositoryId string, level WatchLevel) error
	// Watchers returns the users watching repositoryId at any of levels.
	Watchers(repositoryId string, levels ...WatchLevel) ([]string, error)
	// Ignoring returns those of userIds who ignore repositoryId.
	Ignoring(repositoryId string, userIds []string) (map[string]bool, error)

	Mute(userId, threadType, threadId string) error
	Unmute(userId, threadType, threadId string) error
	// Muted returns those of userIds who unsubscribed from the thread.
	Muted(threadType, threadId string, userIds []string) (map[string]bool, error)
	IsMuted(userId, threadType, threadId string) (bool, error)

	EmailLevel(userId string) (EmailLevel, error) // participating when unset
	SetEmailLevel(userId string, level EmailLevel) error
}
