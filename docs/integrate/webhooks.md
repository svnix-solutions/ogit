# Webhooks

Webhooks send events to your services over HTTP. Payloads and headers follow GitHub's (`X-GitHub-Event`, `X-GitHub-Delivery`, `X-Hub-Signature-256`), so receivers and CI integrations written for GitHub work as they are.

Maintainers and owners manage them in the repository's **Settings → Webhooks**, with the delivery log, each payload and response, and redelivery. Group maintainers can add **group webhooks**, which fire for every repository in the group and its sub-groups.

```bash
R=https://git.example.com/api/v1/repository/me%2Fmy-project
curl -X POST $R/hooks -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"url": "https://ci.example.com/hook", "secret": "s3cret", "events": ["push", "pull_request"]}'
curl $R/hooks/<hook-id>/deliveries -H "Authorization: Bearer $SESSION"                    # newest 100
curl $R/hooks/<hook-id>/deliveries/<id> -H "Authorization: Bearer $SESSION"               # payload and response
curl -X POST $R/hooks/<hook-id>/deliveries/<id>/attempts -H "Authorization: Bearer $SESSION"  # redeliver
curl -X POST $R/hooks/<hook-id>/pings -H "Authorization: Bearer $SESSION"
```

Group webhooks use the same API under `/api/v1/group/<group>/hooks`.

## Events

Subscribe to events by name, or `*` for all. `push` is the default. A `ping` is sent when a hook is created.

| Event | Actions |
|---|---|
| `push` | git pushes over HTTP and SSH, and pull request merges; deleted branches too |
| `pull_request` | opened, edited, closed (`merged: true` for merges), reopened, synchronize (a push to the source branch), converted_to_draft, ready_for_review, labeled, unlabeled, assigned, unassigned, milestoned, demilestoned, review_requested, review_request_removed |
| `pull_request_review` | submitted |
| `pull_request_review_comment` | inline comments created, edited, deleted (those of a review too) |
| `issues` | opened, edited, closed, reopened, labeled, unlabeled, assigned, unassigned, milestoned, demilestoned, deleted |
| `issue_comment` | created, edited, deleted; on pull requests the `issue` has a `pull_request` field, as on GitHub |
| `milestone` | created, edited, closed, opened, deleted |
| `release` | created, published, released, prereleased, edited, deleted, with the assets. Drafts send nothing until published; a tag created by a release is also a `push` |
| `status` | a commit status was posted |
| `repository` | renamed, transferred, publicized, privatized, archived, unarchived, edited |
| `fork` | sent to the original's hooks |
| `gollum` | wiki pages created or edited |

Deliveries are sent in parallel, so their order isn't guaranteed.

## Verifying the signature

With a secret, `X-Hub-Signature-256` is `sha256=` followed by the hex HMAC-SHA256 of the raw body. Compare it in constant time:

```python
import hmac, hashlib

def valid(secret: bytes, body: bytes, header: str) -> bool:
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)
```

Secrets are write-only: the API never returns them.

## Delivery

- Events are queued in the database when they happen, and sent in the background by every server; each delivery goes out once.
- A 2xx answer is a success. Anything else is retried after 30 seconds, 2, 10 and 30 minutes, then marked failed. Redirects aren't followed. Each attempt times out after 10 seconds by default.
- URLs in payloads use the server's `http.public_url`.
- Targets on private, loopback or link-local addresses are refused, unless the administrator lists them in `webhooks.allowed_hosts`. The check is made on the address actually dialed.
