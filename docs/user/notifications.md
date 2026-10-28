# Notifications

Your inbox (`/notifications`, and the bell in the header with the unread count) has one entry per issue, pull request, release or wiki that concerns you. Each new event on it makes it unread again, with what happened.

## Why you get them

- **Participating**, always: you're mentioned, assigned, asked for a review, the author, a reviewer or a commenter, or a check fails on your pull request.
- **Watching**, by repository, from the **Watch** menu on the repository's page:

| Level | You get |
|---|---|
| **Participating** (the default) | only what you take part in |
| **All activity** | also new issues, pull requests, releases and wiki changes |
| **Releases only** | also new releases |
| **Ignore** | nothing, not even mentions |

You watch the repositories you create at "all activity".

Nobody is notified of their own actions, or of anything in a repository they can't read, so an @mention can't reveal a private repository.

## Managing the inbox

Mark entries read one by one or all at once (per repository too), filter by repository, and show only participating ones or everything. **Unsubscribe** from a thread to stop hearing about it; you can subscribe again.

## Email

With your server sending mail, **Settings → Notification emails** chooses which ones are also emailed:

- **participating** (the default): threads you take part in;
- **all**: also what you watch;
- **none**.

## API

```bash
curl "https://git.example.com/api/v1/notifications?all=true" -H "Authorization: Bearer $SESSION"   # unread by default; ?participating=true, ?repository=
curl -X PUT https://git.example.com/api/v1/repository/me%2Fmy-project/watch -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"level": "all"}'
curl -X POST https://git.example.com/api/v1/notifications/read -H "Authorization: Bearer $SESSION"
```
