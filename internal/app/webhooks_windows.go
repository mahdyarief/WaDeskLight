//go:build windows

package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// webhooks.json holds the URL list this window POSTs incoming-message events
// to. One file per machine (shared by all accounts); the secret that signs the
// events is per-account, like the MCP token.

func webhooksPath() string {
	return filepath.Join(getConfigDir(), "webhooks.json")
}

func loadWebhooks() []string {
	b, err := os.ReadFile(webhooksPath())
	if err != nil {
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return nil
	}
	return list
}

func saveWebhooks(list []string) {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(webhooksPath(), b, 0644)
}

func addWebhook(url string) []string {
	url = strings.TrimSpace(url)
	list := loadWebhooks()
	for _, u := range list {
		if u == url {
			return list
		}
	}
	list = append(list, url)
	saveWebhooks(list)
	return list
}

func deleteWebhook(url string) []string {
	list := loadWebhooks()
	out := list[:0]
	for _, u := range list {
		if u != url {
			out = append(out, u)
		}
	}
	saveWebhooks(out)
	return out
}

// webhookSecret returns this account's secret, generating and storing one on
// first use. Receivers verify the X-Wagram-Secret header against it.
func webhookSecret() string {
	name := "webhook-secret"
	if gProfileID != defaultProfileID {
		name = "webhook-secret-" + gProfileID
	}
	path := filepath.Join(getConfigDir(), name)
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	s := randomToken()
	_ = os.WriteFile(path, []byte(s), 0600)
	return s
}

var webhookOnce sync.Once

// startWebhookWatcher polls the chat list every few seconds, and for every
// chat whose unread count went up it POSTs an incoming-message event to each
// configured webhook URL. The loop runs off the UI thread like the scheduler,
// and the agent round trip is bounded by the same timeout as other tools.
func startWebhookWatcher(w webview2.WebView) {
	webhookOnce.Do(func() {
		go func() {
			seen := map[string]int{}
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				pollIncoming(w, seen)
			}
		}()
	})
}

func pollIncoming(w webview2.WebView, seen map[string]int) {
	urls := loadWebhooks()
	if len(urls) == 0 {
		return
	}
	val, err := callAgentString(w, "incoming_events")
	if err != nil {
		return
	}
	var res struct {
		Chats []struct {
			Name    string `json:"name"`
			Preview string `json:"preview"`
			Unread  int    `json:"unread"`
		} `json:"chats"`
	}
	if err := json.Unmarshal([]byte(val), &res); err != nil {
		return
	}
	now := time.Now()
	found := map[string]bool{}
	for _, c := range res.Chats {
		found[c.Name] = true
		prev, ok := seen[c.Name]
		if ok && c.Unread <= prev {
			continue
		}
		seen[c.Name] = c.Unread
		if !ok {
			// First sight after start: record the baseline, do not replay
			// messages that were already unread before the app launched.
			continue
		}
		postWebhooks(urls, map[string]any{
			"event":   "message.incoming",
			"account": gAccountName,
			"profile": gProfileID,
			"service": gServiceBadge,
			"chat":    c.Name,
			"text":    c.Preview,
			"unread":  c.Unread,
			"time":    now.Format("15:04"),
		})
	}
	// Chats that are no longer unread leave the seen table, so the next
	// message on them fires a fresh event.
	for name := range seen {
		if !found[name] {
			delete(seen, name)
		}
	}
}

// callAgentString runs an agent function and returns its value as a JSON
// string, unwrapping the {ok, value, error} envelope.
func callAgentString(w webview2.WebView, fn string) (string, error) {
	res, err := callAgent(w, fn, nil)
	if err != nil {
		return "", err
	}
	var env struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(res, &env); err != nil {
		return string(res), err
	}
	if !env.OK {
		return "", fmt.Errorf("%s", env.Error)
	}
	return env.Value, nil
}

// postWebhooks sends one event to every configured URL, non-blocking, with a
// 5-second timeout and the account secret in a header the receiver can verify.
func postWebhooks(urls []string, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	secret := webhookSecret()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, u := range urls {
		u := u
		go func() {
			req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Wagram-Secret", secret)
			req.Header.Set("X-Wagram-Profile", gProfileID)
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
		}()
	}
}
