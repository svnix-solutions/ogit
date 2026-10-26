package domain

import "time"

// RequestedReviewer is a review requested from a user on a pull request;
// it goes away when they submit a review.
type RequestedReviewer struct {
	PullRequestId string `gorm:"primaryKey"`
	UserId        string `gorm:"primaryKey"`
	User          User   `gorm:"foreignKey:UserId"`
	RequestedById *string
	CreatedAt     time.Time
}

func (RequestedReviewer) TableName() string { return "pull_request_reviewer" }

type RequestedReviewerPers interface {
	// For returns the requested reviewers of pull requests, by id.
	For(pullRequestIds []string) (map[string][]User, error)
	// Set replaces a pull request's requested reviewers.
	Set(pullRequestId string, userIds []string, requestedBy string) error
	// Remove drops userId's request (they reviewed); false if there was none.
	Remove(pullRequestId, userId string) (bool, error)
}
