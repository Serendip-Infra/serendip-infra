package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/serendip-infra/serendip-infra/core/resource"
)

// telegramHost is the only host a Telegram channel contacts: no
// person-typed address is involved (decision 6).
const telegramHost = "api.telegram.org"

// botTokenRE is a bot token's shape. Checking it keeps a stored value
// from changing the request path.
var botTokenRE = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)

var severityMarker = map[resource.Severity]string{
	resource.SeverityCritical: "🔴",
	resource.SeverityWarning:  "🟠",
	resource.SeverityInfo:     "🔵",
}

type telegramButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

type telegramRequest struct {
	ChatID             string `json:"chat_id"`
	Text               string `json:"text"`
	ParseMode          string `json:"parse_mode"`
	LinkPreviewOptions struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
	ReplyMarkup *struct {
		InlineKeyboard [][]telegramButton `json:"inline_keyboard"`
	} `json:"reply_markup,omitempty"`
	ReplyParameters *struct {
		MessageID                int  `json:"message_id"`
		AllowSendingWithoutReply bool `json:"allow_sending_without_reply"`
	} `json:"reply_parameters,omitempty"`
}

type telegramAnswer struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	Result json.RawMessage `json:"result"`
}

// buttonURL says whether Telegram takes the console URL as a button:
// T001 found only the host name localhost refused ("Wrong HTTP URL");
// plain http, loopback IPs and LAN names were accepted.
func buttonURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return false
	}
	return !strings.EqualFold(u.Hostname(), "localhost")
}

// telegramText is the card (decision 6, T001's recorded body). Every
// value from a rule or a Component is escaped.
func telegramText(m Message) string {
	var b strings.Builder
	place := func() string {
		parts := []string{}
		for _, p := range []string{m.Environment, m.Project} {
			if p != "" {
				parts = append(parts, escapeHTML(oneLine(p, 100)))
			}
		}
		return strings.Join(parts, " · ")
	}
	comp := escapeHTML(oneLine(m.Component, 100))
	switch m.Event {
	case EventResolved:
		b.WriteString("<b>✅ RESOLVED</b>")
		if comp != "" {
			b.WriteString(" · " + comp)
		}
		if p := place(); p != "" {
			b.WriteString(" · " + p)
		}
		if m.Detail != "" {
			b.WriteString("\n" + escapeHTML(oneLine(m.Detail, maxDetailLen)))
		}
	case EventTest:
		b.WriteString("<b>🧪 TEST</b>")
		if m.Source != "" {
			b.WriteString(" · " + escapeHTML(oneLine(m.Source, 100)))
		}
		b.WriteString("\nA test notification from Serendip: this channel works.")
	default:
		b.WriteString("<b>" + severityMarker[m.Severity] + " FIRING · " + escapeHTML(string(m.Severity)) + "</b>")
		if comp != "" || place() != "" {
			b.WriteString("\n<b>" + comp + "</b>")
			if p := place(); p != "" {
				b.WriteString(" · " + p)
			}
		}
		if m.Detail != "" {
			b.WriteString("\n" + escapeHTML(oneLine(m.Detail, maxDetailLen)))
		}
		b.WriteString("\nSummary: " + escapeHTML(oneLine(m.Summary, resource.MaxAlertSummaryLen)))
		footer := "Rule " + escapeHTML(oneLine(m.Rule, 100))
		if m.Source != "" {
			footer += " · " + escapeHTML(oneLine(m.Source, 100))
		}
		b.WriteString("\n<i>" + footer + "</i>")
	}
	if m.ConsoleURL != "" && m.Event != EventResolved && !buttonURL(m.ConsoleURL) {
		b.WriteString("\n" + escapeHTML(oneLine(m.ConsoleURL, 500)))
	}
	return b.String()
}

// telegramBody builds sendMessage's body: the card, a "View in console"
// URL button on a firing, and a resolution threaded as a reply.
func telegramBody(chatID string, m Message) ([]byte, error) {
	req := telegramRequest{ChatID: chatID, Text: telegramText(m), ParseMode: "HTML"}
	req.LinkPreviewOptions.IsDisabled = true
	if m.Event != EventResolved && buttonURL(m.ConsoleURL) {
		req.ReplyMarkup = &struct {
			InlineKeyboard [][]telegramButton `json:"inline_keyboard"`
		}{InlineKeyboard: [][]telegramButton{{{Text: "View in console", URL: m.ConsoleURL}}}}
	}
	if m.ThreadRef != "" {
		if id, err := strconv.Atoi(m.ThreadRef); err == nil && id > 0 {
			req.ReplyParameters = &struct {
				MessageID                int  `json:"message_id"`
				AllowSendingWithoutReply bool `json:"allow_sending_without_reply"`
			}{MessageID: id, AllowSendingWithoutReply: true}
		}
	}
	return json.Marshal(req)
}

// telegramCall posts body to a Bot API method. The token is in the URL
// path, so no error here carries the URL (decision 8, RP-069).
func (c *Client) telegramCall(ctx context.Context, token, method string, body []byte) (json.RawMessage, *SendError) {
	if !botTokenRE.MatchString(token) {
		return nil, &SendError{Host: telegramHost, Msg: "the bot token's Secret doesn't hold a bot token"}
	}
	h := http.Header{"Content-Type": {"application/json"}}
	data, se := c.do(ctx, http.MethodPost, c.telegramBase+"/bot"+token+"/"+method, body, h, false, false, token)
	if se != nil {
		se.Host = telegramHost
		// Telegram's own reason and retry_after, when it sent them.
		var ans telegramAnswer
		if json.Unmarshal(se.body, &ans) == nil && ans.Description != "" {
			se.Msg = scrub(oneLine(ans.Description, 200), token)
			if ans.Parameters.RetryAfter > 0 {
				se.RetryAfter = capRetry(time.Duration(ans.Parameters.RetryAfter) * time.Second)
			}
		}
		return nil, se
	}
	var ans telegramAnswer
	if err := json.Unmarshal(data, &ans); err != nil || !ans.OK {
		return nil, &SendError{Host: telegramHost, Msg: "unexpected answer"}
	}
	return ans.Result, nil
}

// sendTelegram sends m and returns the message_id a resolution replies to.
func (c *Client) sendTelegram(ctx context.Context, s resource.TelegramSettings, token string, m Message) (string, *SendError) {
	body, err := telegramBody(s.ChatID, m)
	if err != nil {
		return "", &SendError{Host: telegramHost, Msg: "encoding the message failed"}
	}
	res, se := c.telegramCall(ctx, token, "sendMessage", body)
	if se != nil {
		return "", se
	}
	var msg struct {
		MessageID int `json:"message_id"`
	}
	if json.Unmarshal(res, &msg) != nil || msg.MessageID == 0 {
		return "", nil
	}
	return strconv.Itoa(msg.MessageID), nil
}

// TelegramChat is a chat the bot has seen a message from.
type TelegramChat struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

// FindTelegramChats reads getUpdates once (the console's "Find chat"
// step): T001 found a chat appears only after the person messages the
// bot. The chats are returned once each, newest first.
func (c *Client) FindTelegramChats(ctx context.Context, token string) ([]TelegramChat, error) {
	res, se := c.telegramCall(ctx, token, "getUpdates", []byte(`{"limit":100,"timeout":0}`))
	if se != nil {
		return nil, se
	}
	type chat struct {
		ID        int64  `json:"id"`
		Type      string `json:"type"`
		Title     string `json:"title"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
	}
	var updates []map[string]json.RawMessage
	if err := json.Unmarshal(res, &updates); err != nil {
		return nil, errors.New(telegramHost + ": unexpected answer")
	}
	seen := map[int64]bool{}
	var out []TelegramChat
	for i := len(updates) - 1; i >= 0; i-- {
		for _, key := range []string{"message", "channel_post", "my_chat_member", "edited_message"} {
			raw, ok := updates[i][key]
			if !ok {
				continue
			}
			var v struct {
				Chat chat `json:"chat"`
			}
			if json.Unmarshal(raw, &v) != nil || v.Chat.ID == 0 || seen[v.Chat.ID] {
				continue
			}
			seen[v.Chat.ID] = true
			title := v.Chat.Title
			if title == "" {
				title = strings.TrimSpace(v.Chat.FirstName + " " + v.Chat.LastName)
			}
			if title == "" && v.Chat.Username != "" {
				title = "@" + v.Chat.Username
			}
			out = append(out, TelegramChat{ID: strconv.FormatInt(v.Chat.ID, 10), Type: v.Chat.Type, Title: oneLine(title, 100)})
		}
	}
	return out, nil
}
