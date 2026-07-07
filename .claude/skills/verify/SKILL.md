---
name: verify
description: Build, launch, and drive the Dispatch app end-to-end (Postgres + Temporal + worker + server + web UI) to verify changes at the real surface.
---

# Verifying Dispatch

## Launch (in order)

```bash
docker compose up -d               # Postgres :5432, Temporal :7233 (UI :8233)
go run ./cmd/migrate               # idempotent
DISPATCH_FAKE_LLM=1 go run ./cmd/worker   # background; scripted demo LLM, no API key
go run ./cmd/server                # background; JSON API :8080
cd web && npm run dev              # background; UI :5173, proxies /api → :8080
```

Ready checks: `curl -s localhost:8080/api/conversations` and `curl -s -o /dev/null -w "%{http_code}" localhost:5173/`.

## Drive (headless browser)

Playwright works but needs two workarounds on this WSL2 box (no sudo):

- Install `playwright` in a scratchpad dir (`npm i playwright`), matching the
  browser build in `~/.cache/ms-playwright/`.
- Chromium fails with `libnspr4.so: cannot open shared object file`. Fix
  without sudo: `apt-get download libnspr4 libnss3 libasound2t64`, extract
  with `dpkg -x <deb> extracted/`, then run node with
  `LD_LIBRARY_PATH=$PWD/extracted/usr/lib/x86_64-linux-gnu`.

## Flows worth driving

- Home → simulator form → "Send as customer" → navigates to the conversation.
- Fake-LLM script: first message → `update_job` (auto-executed) +
  `send_message` (pending). Approve → agent bubble appears, ticket collapses.
- Reject requires a non-empty reason (button disabled otherwise); Edit with
  invalid JSON shows an inline error and sends nothing.
- Sending another inbound message while the run is open continues the same
  workflow; on a closed conversation it starts a fresh run.

## Gotchas

- The UI polls (1.5s / 3s) — wait ~3s after a decision before screenshotting.
- shadcn base-ui overlays (sheet, dropdown) intercept clicks while open;
  close them before clicking elsewhere or Playwright times out.
- Data persists in the Postgres volume across sessions; expect old
  conversations in the list.
