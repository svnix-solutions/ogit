# Issues

Issues track bugs, ideas and work. They share their numbers with pull requests, so `#12` is one or the other.

## Who does what

- Anyone who can read the repository opens issues and comments.
- The author and developers and up edit, close and reopen them.
- Developers and up set labels, assignees and the milestone.
- Maintainers and up delete issues and others' comments, and manage labels (**Settings → Labels**) and milestones.

```bash
R=https://git.example.com/api/v1/repository/me%2Fmy-project
curl -X POST $R/issues -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"title": "Login fails on Safari", "body": "Steps: ..."}'
curl -X PATCH $R/issues/12 -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' -d '{"state": "closed"}'
```

## Finding issues

The list filters by state (open, closed, all), label, author, assignee (someone, or nobody), milestone (one, or none), "assigned to me" and "created by me", and searches titles and descriptions. The counts per state follow the other filters.

```bash
curl "$R/issues?state=open&label=bug&assignee=alice&q=safari"
```

## Labels, assignees and milestones

They work the same for issues and pull requests:

- **Labels:** the repository's labels, with a name, color and description.
- **Assignees:** up to 10 people with write access.
- **Milestones:** a title, a description and an optional due date. Each milestone counts its open and closed issues and pull requests, and can be closed when it's done.

## Closing from a pull request

"Fixes #12" in a pull request's title or description (also close, closes, closed, fix, fixed, resolve, resolves, resolved) closes issue #12 when the pull request is merged into the default branch.

## Turning issues off

Owners can turn issues off in **Settings → General**. Existing issues stay readable, but nothing can be added or changed until they're turned back on.

## Notifications

The author, assignees, commenters and anyone @mentioned hear about others' activity, and watchers at "all activity" about new issues. See [Notifications](./notifications).
