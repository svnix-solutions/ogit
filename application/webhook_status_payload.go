package application

import (
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
)

// GitHub's "status" event.

type StatusPayload struct {
	Id          string             `json:"id"`
	SHA         string             `json:"sha"`
	Name        string             `json:"name"` // the repository's full name
	TargetURL   *string            `json:"target_url"`
	Context     string             `json:"context"`
	Description *string            `json:"description"`
	State       domain.StatusState `json:"state"`
	Commit      HookStatusCommit   `json:"commit"`
	Branches    []HookStatusBranch `json:"branches"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
	Repository  HookRepository     `json:"repository"`
	Sender      HookUser           `json:"sender"`
}

type HookStatusCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name  string    `json:"name"`
			Email string    `json:"email"`
			Date  time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

type HookStatusBranch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// StatusEvent queues a status event for s on commit.
func (c *WebhookApp) StatusEvent(repo *domain.Repository, refs storer.ReferenceStorer, commit *object.Commit, s *domain.CommitStatus, actorId string) {
	if c == nil {
		return
	}
	hooks, err := c.Hooks.List(repo.Id)
	if err != nil || !anyWants(hooks, "status") {
		return // nothing subscribed: skip listing branches
	}
	p := StatusPayload{
		Id: s.Id, SHA: s.SHA, Name: repo.FullPath(), TargetURL: optional(s.TargetURL), Context: s.Context,
		Description: optional(s.Description), State: s.State, Branches: []HookStatusBranch{},
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Repository: c.repositoryPayload(repo), Sender: c.sender(actorId, ""),
	}
	p.Commit.SHA, p.Commit.HTMLURL = s.SHA, c.baseURL()+"/"+repo.FullPath()+"/commit/"+s.SHA
	p.Commit.Commit.Message = commit.Message
	p.Commit.Commit.Author.Name, p.Commit.Commit.Author.Email, p.Commit.Commit.Author.Date = commit.Author.Name, commit.Author.Email, commit.Author.When
	// The branches whose tip is the commit, as GitHub lists them.
	if iter, err := refs.IterReferences(); err == nil {
		_ = iter.ForEach(func(r *plumbing.Reference) error {
			if r.Name().IsBranch() && r.Type() == plumbing.HashReference && r.Hash().String() == s.SHA && len(p.Branches) < 10 {
				b := HookStatusBranch{Name: r.Name().Short()}
				b.Commit.SHA = s.SHA
				p.Branches = append(p.Branches, b)
			}
			return nil
		})
	}
	c.emit(repo, "status", "", p)
}
