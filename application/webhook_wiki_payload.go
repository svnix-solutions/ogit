package application

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/domain"
)

// GollumPayload is GitHub's gollum event: wiki pages created or edited.
type GollumPayload struct {
	Pages      []HookWikiPage `json:"pages"`
	Repository HookRepository `json:"repository"`
	Sender     HookUser       `json:"sender"`
}

type HookWikiPage struct {
	PageName string  `json:"page_name"`
	Title    string  `json:"title"`
	Summary  *string `json:"summary"`
	Action   string  `json:"action"` // created or edited
	Sha      string  `json:"sha"`
	HTMLURL  string  `json:"html_url"`
}

// Gollum sends the gollum event for pages of wiki changed by commit.
func (c *WebhookApp) Gollum(wiki *domain.Repository, pusher Pusher, commit plumbing.Hash, pages []wikiPageChange) {
	if c == nil || len(pages) == 0 {
		return
	}
	p := GollumPayload{Repository: c.repositoryPayload(wiki), Sender: c.sender(pusher.UserId, pusher.Name)}
	for _, pg := range pages {
		p.Pages = append(p.Pages, HookWikiPage{PageName: pg.name, Title: pageTitle(pg.name), Action: pg.action, Sha: commit.String(),
			HTMLURL: c.baseURL() + "/" + wiki.FullPath() + "/wiki/" + escapePath(pg.name)})
	}
	// The repository's hooks and workflows (on: gollum) get it.
	c.emit(domain.RepositoryOfWiki(wiki), "gollum", "", p)
}
