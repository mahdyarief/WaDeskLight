//go:build windows

package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// The MCP server exposes this window's page to AI agents over JSON-RPC 2.0 on
// a loopback HTTP endpoint. It is implemented with the standard library only
// because the project vendors its dependencies and adds none.
const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type mcpEndpoint struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

type mcpServer struct {
	w     webview2.WebView
	token string
}

// mcpListenAddr is the fixed loopback address the endpoint binds to, so an
// agent's config stays valid across launches. If it is already taken (a second
// account, or another app), the server falls back to a random free port and
// records the actual address in the discovery file.
const mcpListenAddr = "127.0.0.1:5987"

var mcpStartOnce sync.Once

// startMCPServer launches the loopback MCP endpoint once per process.
func startMCPServer(w webview2.WebView) {
	mcpStartOnce.Do(func() {
		ln, err := net.Listen("tcp", mcpListenAddr)
		if err != nil {
			ln, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return
			}
		}
		srv := &mcpServer{w: w, token: persistentToken()}
		mux := http.NewServeMux()
		mux.HandleFunc("/mcp", srv.handle)
		go func() { _ = http.Serve(ln, mux) }()
		writeMCPEndpoint(ln.Addr().String(), srv.token)
	})
}

// persistentToken returns this machine's MCP token, generating and storing one
// on first use. Keeping it (rather than rotating per launch) lets an agent's
// config keep working across restarts.
func persistentToken() string {
	path := filepath.Join(getConfigDir(), "mcp-token")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	}
	t := randomToken()
	_ = os.WriteFile(path, []byte(t), 0600)
	return t
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// writeMCPEndpoint records the URL and token so an agent can find and
// authenticate to this window. One file per account keeps concurrent windows
// from overwriting each other.
func writeMCPEndpoint(addr, token string) {
	ep := mcpEndpoint{URL: "http://" + addr + "/mcp", Token: token}
	b, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return
	}
	name := "mcp.json"
	if gProfileID != defaultProfileID {
		name = "mcp-" + gProfileID + ".json"
	}
	_ = os.WriteFile(filepath.Join(getConfigDir(), name), b, 0600)
}

func (s *mcpServer) handle(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rw.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) != "Bearer "+s.token {
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(rw, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	resp, notify := s.dispatch(req)
	if notify {
		rw.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(rw, resp)
}

func (s *mcpServer) dispatch(req rpcRequest) (rpcResponse, bool) {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "wagramdesklite", "version": "1.0.0"},
		}
	case "notifications/initialized", "notifications/cancelled":
		return resp, true
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		resp.Result = s.callTool(req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, false
}

func (s *mcpServer) callTool(params json.RawMessage) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return toolError("invalid params: " + err.Error())
	}
	switch p.Name {
	case "app_status":
		return toolText(s.statusText())
	case "list_chats":
		return s.agentTool("list_chats", p.Arguments)
	case "open_chat":
		return s.agentTool("open_chat", p.Arguments)
	case "read_messages":
		return s.agentTool("read_messages", p.Arguments)
	case "send_message":
		return s.agentTool("send_message", p.Arguments)
	case "export_chat":
		return s.agentTool("read_messages", p.Arguments)
	default:
		return toolError("unknown tool: " + p.Name)
	}
}

// agentTool drives the page through the injected agent and unwraps its
// {ok, value|error} envelope into an MCP tool result.
func (s *mcpServer) agentTool(fn string, args json.RawMessage) any {
	var a any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			a = nil
		}
	}
	res, err := callAgent(s.w, fn, a)
	if err != nil {
		return toolError(err.Error())
	}
	var env struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error string          `json:"error"`
	}
	if err := json.Unmarshal(res, &env); err != nil {
		return toolText(string(res))
	}
	if !env.OK {
		return toolError(env.Error)
	}
	return toolText(string(env.Value))
}

func (s *mcpServer) statusText() string {
	info := map[string]any{
		"account": gAccountName,
		"service": gServiceBadge,
	}
	if res, err := callAgent(s.w, "status", nil); err == nil {
		info["page"] = json.RawMessage(res)
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func toolText(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
}

func toolError(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": true,
	}
}

func writeRPC(rw http.ResponseWriter, resp rpcResponse) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(resp)
}

func mcpTools() []map[string]any {
	return []map[string]any{
		{
			"name":        "app_status",
			"description": "Report which service and account this window shows, and whether the chat page is ready.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "list_chats",
			"description": "List the chats visible in the chat list, with name, last-message preview, and unread count.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "open_chat",
			"description": "Open a conversation by name so read_messages and send_message can act on it. Matching is case-insensitive.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "The chat name to open, as returned by list_chats."},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "read_messages",
			"description": "Read the messages of the currently open conversation.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "description": "Maximum number of recent messages to return."},
				},
			},
		},
		{
			"name":        "send_message",
			"description": "Send a text message in the currently open conversation.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "description": "The message text to send."},
				},
				"required": []string{"text"},
			},
		},
		{
			"name":        "export_chat",
			"description": "Return the messages of the currently open conversation for export.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer"},
				},
			},
		},
	}
}
