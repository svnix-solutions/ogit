package repository

import (
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config        config.Config
	Logger        zerolog.Logger
	FiberOapi     *fiberoapi.OApiGroup
	RepositoryApp *application.RepositoryApp
}

func SetupRepositoryRouter(controller Controller) {
	fiberoapi.Get(controller.FiberOapi, "/:identifier", controller.GetRepository, fiberoapi.OpenAPIOptions{
		Summary:     "Get repository",
		Description: "Retrieve the details of a specific repository",
		OperationID: "repository.get",
		Tags:        []string{"Repository"},
	})

	fiberoapi.Post(controller.FiberOapi, "", controller.CreateRepository, fiberoapi.OpenAPIOptions{
		Summary:     "Create repository",
		Description: "Create a new repository",
		OperationID: "repository.create",
		Tags:        []string{"Repository"},
	})

	fiberoapi.Get(controller.FiberOapi, "", controller.ListRepositories, fiberoapi.OpenAPIOptions{
		Summary:     "List my repositories",
		Description: "Repositories the caller is a member of, directly or through a group",
		OperationID: "repository.list",
		Tags:        []string{"Repository"},
	})

	fiberoapi.Get(controller.FiberOapi, "/:identifier/members", controller.ListMembers, fiberoapi.OpenAPIOptions{
		Summary:     "List repository members",
		Description: "Users and groups with access. Visible to anyone who can read the repository.",
		OperationID: "repository.members.list",
		Tags:        []string{"Repository members"},
	})

	fiberoapi.Post(controller.FiberOapi, "/:identifier/members", controller.AddMember, fiberoapi.OpenAPIOptions{
		Summary: "Add repository member",
		Description: "Add a user (\"username\") or a group you belong to (\"group\") with a role: reporter, developer, maintainer or owner (users only). " +
			"Owners can grant any role; maintainers up to developer.",
		OperationID: "repository.members.add",
		Tags:        []string{"Repository members"},
	})

	fiberoapi.Patch(controller.FiberOapi, "/:identifier/members/:member_id", controller.UpdateMember, fiberoapi.OpenAPIOptions{
		Summary:     "Change a member's role",
		Description: "Owners can change any member; maintainers only members up to developer. A repository always keeps one owner.",
		OperationID: "repository.members.update",
		Tags:        []string{"Repository members"},
	})

	fiberoapi.Delete(controller.FiberOapi, "/:identifier/members/:member_id", controller.RemoveMember, fiberoapi.OpenAPIOptions{
		Summary:     "Remove a member",
		Description: "Owners can remove anyone; maintainers members up to developer; anyone can remove themselves. The last owner cannot be removed.",
		OperationID: "repository.members.remove",
		Tags:        []string{"Repository members"},
	})
}
