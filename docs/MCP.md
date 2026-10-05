# MCP Access

Every WaGramDeskLite window also runs a small **Model Context Protocol (MCP)**
server, so an AI agent can read the chat list, read the messages of the open
conversation, and send a message — through the same UI you are using, no extra
login.

The server is implemented with the Go standard library only
(`internal/app/mcp_windows.go`); it adds no dependencies.

## How it works

- On startup each window listens on a **loopback** address, `127.0.0.1`, on a
  **random free port**. Nothing is exposed to your network.
- It speaks MCP over JSON-RPC 2.0 at the path `/mcp`, protocol version
  `2025-06-18`.
- Every request must carry the header `Authorization: Bearer <token>`.
- The tools drive the page through the injected agent (`internal/app/agent_windows.go`),
  so the agent sees exactly the DOM the window is rendering.

## Where the token comes from

The token is generated fresh at every launch by `randomToken()` in
`internal/app/mcp_windows.go`:

- 24 bytes drawn from `crypto/rand`, hex-encoded — a 48-character hex string.
- It is **not** persisted. It lives in memory for the life of the process and is
  written to a discovery file so an agent can read it.
- Because it is regenerated on each start, the **token and the port rotate every
  launch**. There is no long-lived credential to leak; nothing to revoke.

If `crypto/rand` ever fails, the fallback is the process start time in
nanoseconds — still unique, but the normal path is the random one.

## Discovery file

The window records its URL and token so an agent can find it. The file is
written with mode `0600` (readable only by your user) under
`%APPDATA%\WaGramDeskLite\`:

| Account | File |
|---|---|
| First account (default profile) | `mcp.json` |
| Additional accounts | `mcp-<profile-id>.json` |

One file per account keeps concurrent windows from overwriting each other.

Contents:

```json
{
  "url": "http://127.0.0.1:61956/mcp",
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
| `send_message` | `text` (string, required) | Sends `text` in the open conversation |
| `export_chat` | `limit` (integer) | Same shape as `read_messages`, intended for export |

`read_messages` and `export_chat` return an empty list when no conversation is
open, and `send_message` needs one open too. `app_status` reports `ready: true`
only once the composer is present, which is the signal that a conversation is
open. `open_chat` matches the name case-insensitively, with a substring fallback.

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

The URL and token rotate on every launch, so an agent should read `mcp.json`
each time instead of hard-coding them. The app must be running for the endpoint
to exist.

### HTTP transport (Claude Desktop, Cursor, and other remote-MCP clients)

Point the client's MCP config at the discovery file's `url` and pass the token
as a header:

```json
{
  "mcpServers": {
    "wagramdesklite": {
      "type": "http",
      "url": "http://127.0.0.1:61956/mcp",
      "headers": { "Authorization": "Bearer 4568ca030bfaf29c780531d238a03b346fb021733fd07743" }
    }
  }
}
```

After each app restart, refresh the `url` and `token` from `mcp.json`.

### stdio-only clients

Some clients can only launch a command and talk over stdio. For those, wrap the
HTTP endpoint in a small bridge that reads `mcp.json`, forwards each JSON-RPC
message to `url` with the `Authorization: Bearer <token>` header, and writes the
responses back to stdout. The loopback HTTP endpoint above is the source of
truth; no bridge ships with this repository yet.

