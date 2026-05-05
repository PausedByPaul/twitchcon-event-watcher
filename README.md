# TwitchCon Event Watcher

A lightweight Go application that polls the TwitchCon sessions API once an hour and reports any schedule changes to a Discord channel via webhook.

## Features

- Detects **added**, **removed**, and **updated** sessions
- Posts rich Discord embeds with colour-coded change types:
  - ➕ **Green** — new session added
  - ❌ **Red** — session removed
  - ✏️ **Amber** — session details changed, with a field-by-field diff
- Persists state to a local JSON file so only new changes are reported across restarts
- Handles Discord's rate limits and message size constraints automatically
- No external dependencies — uses only the Go standard library

## Requirements

- Go 1.22 or later
- A Discord webhook URL

## Setup

1. **Clone the repository:**
   ```sh
   git clone https://github.com/PausedByPaul/twitchcon-event-watcher.git
   cd twitchcon-event-watcher
   ```

2. **Create a Discord webhook** in your server:
   - Channel Settings → Integrations → Webhooks → New Webhook
   - Copy the webhook URL

3. **Set the required environment variable:**
   ```sh
   # Windows (PowerShell)
   $env:DISCORD_WEBHOOK_URL = "https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN"

   # Linux / macOS
   export DISCORD_WEBHOOK_URL="https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN"
   ```

## Running

```sh
go run .
```

Or build a binary first:

```sh
go build -o twitchcon-watcher .
./twitchcon-watcher         # Linux / macOS
.\twitchcon-watcher.exe     # Windows
```

On **first run** the app saves a baseline snapshot (`sessions_state.json`) and sends no notification — this prevents a flood of "added" messages for all existing sessions. From the second poll onwards, only genuine changes are reported.

## Configuration

All configuration is done through environment variables. See [.env.example](.env.example) for a template.

| Variable | Required | Default | Description |
|---|---|---|---|
| `DISCORD_WEBHOOK_URL` | ✅ | — | Discord incoming webhook URL |
| `POLL_INTERVAL` | | `1h` | How often to poll the API. Accepts any Go duration string (e.g. `30m`, `2h`) |
| `STATE_FILE` | | `sessions_state.json` | Path to the local state file |
| `WEBHOOK_USERNAME` | | `TwitchCon Watcher` | Display name for the Discord bot |
| `API_URL` | | `https://api.twitchcon.com/sessions?eventName=rotterdam-2026` | TwitchCon sessions API endpoint |

## Running as a Service

### systemd (Linux)

Create `/etc/systemd/system/twitchcon-watcher.service`:

```ini
[Unit]
Description=TwitchCon Event Watcher
After=network.target

[Service]
ExecStart=/usr/local/bin/twitchcon-watcher
Environment=DISCORD_WEBHOOK_URL=https://discord.com/api/webhooks/YOUR_ID/YOUR_TOKEN
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now twitchcon-watcher
```

### Task Scheduler (Windows)

You can also run this with Windows Task Scheduler set to trigger on system startup, pointing at the compiled `.exe` with the `DISCORD_WEBHOOK_URL` environment variable set in the task's environment.

## How Change Detection Works

The app tracks sessions by their `sessionId`. On each poll:

1. New IDs → **Added**
2. Missing IDs → **Removed**
3. Same ID, different content → **Updated** (compares title, date, time, location, program, speakers, tags, description, featured, and private flags)

Speaker and tag lists are sorted before comparison so reordering alone doesn't trigger an update notification.
