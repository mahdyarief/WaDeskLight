# MCP Access

Every WaGramDeskLite window also runs a small **Model Context Protocol (MCP)**
server, so an AI agent can read the chat list, read the messages of the open
conversation, and send a message — through the same UI you are using, no extra
login.

The server is implemented with the Go standard library only
(`internal/app/mcp_windows.go`); it adds no dependencies.

## How it works

- On startup each window listens on a **loopback** address, `127.0.0.1:5987`.
  Nothing is exposed to your network. If that port is already taken (a second
  account, or another app), the window falls back to a random free port and
  records the actual one in the discovery file.
- It speaks MCP over JSON-RPC 2.0 at the path `/mcp`, protocol version
  `2025-06-18`.
- Every request must carry the header `Authorization: Bearer <token>`.
- The tools drive the page through the injected agent (`internal/app/agent_windows.go`),
  so the agent sees exactly the DOM the window is rendering.

## Where the token comes from

Each account gets its own token, so an agent bound to one account's endpoint
cannot reuse its credential against another account's. The token is generated
once and then **persisted**, so an agent's config keeps working across
restarts. On first launch `persistentToken()` in `internal/app/mcp_windows.go`
writes the default account's token to `%APPDATA%\WaGramDeskLite\mcp-token`
and an additional account's token to `mcp-token-<profile-id>` (both mode
`0600`); every later launch reads that file back instead of rotating.

- The token itself is 24 bytes drawn from `crypto/rand`, hex-encoded — a
  48-character hex string.
- To see the current token, read the account's token file (`mcp-token` for
  the default account, `mcp-token-<profile-id>` for another), or the `token`
  field of its discovery file below (both hold the same value).
- To rotate it, delete the account's token file and restart the app; a new
  one is generated for that account.

If `crypto/rand` ever fails, the fallback is the process start time in
nanoseconds — still unique, but the normal path is the random one.

## Discovery file

The window records its URL and token so an agent can find it. The file is
written with mode `0600` (readable only by your user) under
`%APPDATA%\WaGramDeskLite\`:

| Account | Discovery file | Token file |
|---|---|---|
| First account (default profile) | `mcp.json` | `mcp-token` |
| Additional accounts | `mcp-<profile-id>.json` | `mcp-token-<profile-id>` |

One file per account keeps concurrent windows from overwriting each other, and
each account authenticates with its own token. The port is fixed, so `mcp.json`
only changes if the fixed port was taken and the window fell back to a random
one; an additional account always gets its own free port.

Contents:

```json
{
  "url": "http://127.0.0.1:5987/mcp",
  "token": "4568ca030bfaf29c780531d238a03b346fb021733fd07743"
}
```

To locate the endpoint from a script:

```bash
cat "$APPDATA/WaGramDeskLite/mcp.json"
```

## Tools

| Tool | Arguments | Returns |
|---|---|---|
| `app_status` | none | Account name, service badge, and the page's `service` / `ready` / `title` |
| `list_chats` | none | Visible chats with `name`, `preview`, `unread`, `active` |
| `open_chat` | `name` (string, required) | Opens the matching chat, so `read_messages` and `send_message` target it |
| `read_messages` | `limit` (integer, default 50) | Recent messages of the open conversation: `text`, `time`, `outgoing` |
| `conversation_summary` | `limit` (integer, default 50) | Reply-decision data for the open conversation: `total`, `incoming_count`, `outgoing_count`, `last_message`, `should_reply` (true when the last message is incoming), plus the full `messages` list |
| `send_message` | `text` (string, required) | Sends `text` in the open conversation |
| `export_chat` | `limit` (integer) | Same shape as `read_messages`, intended for export |

`read_messages` and `export_chat` return an empty list when no conversation is
open, and `send_message` needs one open too. `app_status` reports `ready: true`
only once the composer is present, which is the signal that a conversation is
open. `open_chat` matches the name case-insensitively, with a substring fallback.
When the name is not in the rendered list (WhatsApp virtualizes the list, so
rows below the fold are absent from the DOM), `open_chat` types the name into
the left-pane search box and returns `{"opened":false,"searched":true}`; call it
again after a moment and the filtered row opens.

To read or reply to a chat that is not the one currently open, call `open_chat`
first, then `read_messages` or `send_message`. There is no server-initiated
event stream: an agent detects incoming messages by polling `list_chats` (each
row carries its `unread` count and last-message `preview`) and opening the ones
that matter.

## Manual test

With the app running and logged in:

```bash
EP=$(cat "$APPDATA/WaGramDeskLite/mcp.json")
URL=$(echo "$EP" | python -c "import sys,json;print(json.load(sys.stdin)['url'])")
TOKEN=$(echo "$EP" | python -c "import sys,json;print(json.load(sys.stdin)['token'])")

curl -s -X POST "$URL" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  --data '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"app_status","arguments":{}}}'
```

## Connecting an agent

The port is fixed (`5987`) and the token is persisted, so an agent's config can
be written once and keep working across restarts. Read `mcp.json` if the fixed
port was taken and the window fell back to a random one. For an additional
account, read `mcp-<profile-id>.json` instead — its port is a free random one
and its token comes from `mcp-token-<profile-id>`. The app must be running
for the endpoint to exist.

### HTTP transport (Claude Desktop, Cursor, and other remote-MCP clients)

Point the client's MCP config at the discovery file's `url` and pass the token
as a header:

```json
{
  "mcpServers": {
    "wagramdesklite": {
      "type": "http",
      "url": "http://127.0.0.1:5987/mcp",
      "headers": { "Authorization": "Bearer 4568ca030bfaf29c780531d238a03b346fb021733fd07743" }
    }
  }
}
```

This config is stable: the URL and token no longer change on restart. If you
ever want to rotate the token, delete `mcp-token` and restart the app.

### stdio-only clients

Some clients can only launch a command and talk over stdio. For those, wrap the
HTTP endpoint in a small bridge that reads `mcp.json`, forwards each JSON-RPC
message to `url` with the `Authorization: Bearer <token>` header, and writes the
responses back to stdout. The loopback HTTP endpoint above is the source of
truth; no bridge ships with this repository yet.

