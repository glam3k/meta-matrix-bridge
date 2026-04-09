# Story Reply Offload Design

## Background
Messenger story replies fail for E2EE-only contacts because the bridge only calls the classic `useStoriesSendReplyMutation`. The encrypted web client instead runs a MAW/Lightspeed mutation encapsulated in a browser session, which we have been unable to reverse engineer reliably. To unblock personal deployments, we'll add an escape hatch that delegates Messenger story replies to a tiny helper service which drives a headless browser using the user's existing cookies.

## Goals
- Allow replying to Messenger stories in E2EE chats without reverse-engineering MAW payloads.
- Keep the helper isolated so the bridge core stays unchanged for non-E2EE replies.
- Reuse the user's logged-in cookies; no separate auth flow.
- Provide a simple request/response contract so the bridge can report success/failure back to Matrix.

## Non-Goals
- Full automation coverage for every Facebook action.
- Handling large reply volumes (this is a rare fallback path).
- Running in multi-tenant/hosted environments.

## High-Level Architecture
1. The bridge continues to try the classic GraphQL mutation.
2. If the thread is E2EE (based on portal metadata or GraphQL failure code 1675030), the bridge POSTs a JSON payload to a local "story-reply helper" service.
3. The helper launches (or reuses) a headless browser session, injects the provided cookies, opens the story permalink, types the reply, and hits send.
4. The helper replies with `{status:"ok", fbStoryID: "..."}` or `{status:"error", reason: "..."}`.
5. The bridge logs success/failure and updates Matrix accordingly.

## Bridge Changes
- Add config block `stories.helper_url` with enable flag and request timeout.
- Extend `tryMessengerStoryReply`:
  - Detect E2EE portals via metadata or GraphQL error.
  - Construct helper payload with story metadata (ID, reel ID, owner, message body) plus serialized cookies.
  - Call helper; on success, fabricate a `MatrixMessageResponse` similar to Instagram path. On failure, surface error to Matrix.
- Implement cookie exporter that captures the current `c_user`, `xs`, `fr`, `datr`, `sb`, `spin` values from the logged-in session. (We already have them inside `messagix`.)

## Helper Service Design
- Language: Node.js + Playwright (stable Chromium bundle, good API for DOM scripting).
- REST endpoint: `POST /reply`
  ```json
  {
    "story_url": "https://www.facebook.com/stories/...",
    "message": "Nice photo!",
    "cookies": [{"name":"c_user","value":"...","domain":".facebook.com"}, ...],
    "timeout_ms": 15000
  }
  ```
- Steps per request:
  1. Create isolated browser context; add cookies.
  2. Navigate to `story_url`.
  3. Wait for reply composer (`textarea[aria-label="Reply"]` or similar) with retries.
  4. Type message, click send button, wait for DOM indication of success.
  5. Return JSON response. Clean up context.
- Logging: structured logs for navigation, DOM selectors, and Facebook errors (e.g., blocked stories, expired stories).
- Optional optimization: maintain a browser pool to avoid cold boots, but keep concurrency low.

## Deployment
- Build a minimal container (Node + Playwright dependencies). Expose on localhost inside the same host as the bridge.
- Pass cookies via shared volume or HTTP header from the bridge. Since this is personal use, a simple shared secret/token for the helper endpoint is enough.
- Configure systemd/docker-compose to start helper alongside the bridge. Ensure Chromium sandbox permissions are satisfied on the host.

## Testing Strategy
- Unit-test helper payload construction in Go (ensure correct story URL, cookie serialization, timeout behavior).
- Integration-test helper locally by feeding known cookies and a mock story URL; verify it can post a reply in a throwaway account.
- Add bridge logs/metrics counting helper invocations, latency, and failures for observability.

## Risks & Mitigations
- **DOM changes break automation**: keep selectors centralized, log descriptive errors, and maintain manual override.
- **Cookie leaks**: restrict helper to localhost, require a shared secret, and avoid persisting cookies on disk.
- **Chromium resource usage**: add per-request timeout and concurrency cap to prevent runaway processes.

## Future Work
- Continue attempting to decode MAW/Lightspeed story-reply mutation for a native solution.
- Expand helper to support attachments or reactions if needed.
- Replace cookie passing with a lightweight OAuth-like token if multi-user support is desired later.
