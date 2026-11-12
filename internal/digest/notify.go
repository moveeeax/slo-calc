package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Message is what a deliverer sends: the compact summary, the per-SLO
// detail for the thread, and whether it should notify.
type Message struct {
	Summary string
	Detail  string
	Loud    bool
}

// Deliverer posts a message somewhere.
type Deliverer interface {
	Deliver(ctx context.Context, m Message) error
}

// Message size limits. Telegram rejects texts over 4096 characters after
// entity parsing; Slack's chat.postMessage docs recommend keeping text
// under 4000 characters and truncate past 40000. Detail longer than this is
// split on line boundaries into several thread replies.
const (
	telegramLimit = 4096
	slackLimit    = 4000
)

const maxReplyBytes = 1 << 20

func httpClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// SlackWebhook posts to a Slack incoming webhook. Webhooks cannot reply in
// a thread, so the detail follows the summary as separate messages.
type SlackWebhook struct {
	URL  string
	HTTP *http.Client
}

// Deliver implements Deliverer.
func (s *SlackWebhook) Deliver(ctx context.Context, m Message) error {
	for _, text := range append(chunks(m.Summary, slackLimit), chunks(m.Detail, slackLimit)...) {
		body, status, err := postJSON(ctx, s.HTTP, s.URL, "", map[string]any{"text": text})
		if err != nil {
			return fmt.Errorf("slack webhook: %w", err)
		}
		// A webhook answers 200 with the body "ok", and 4xx with an
		// error code such as invalid_payload or no_service.
		if status != http.StatusOK {
			return fmt.Errorf("slack webhook: http %d: %s", status, strings.TrimSpace(string(body)))
		}
	}
	return nil
}

// SlackBot posts with chat.postMessage and a bot token, putting the detail
// in a thread under the summary.
type SlackBot struct {
	Token   string
	Channel string
	// BaseURL defaults to https://slack.com/api.
	BaseURL string
	HTTP    *http.Client
}

type slackReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	TS    string `json:"ts"`
}

// Deliver implements Deliverer.
func (s *SlackBot) Deliver(ctx context.Context, m Message) error {
	base := s.BaseURL
	if base == "" {
		base = "https://slack.com/api"
	}
	post := func(text, thread string) (string, error) {
		payload := map[string]any{"channel": s.Channel, "text": text}
		if thread != "" {
			payload["thread_ts"] = thread
		}
		body, status, err := postJSON(ctx, s.HTTP, base+"/chat.postMessage", "Bearer "+s.Token, payload)
		if err != nil {
			return "", fmt.Errorf("slack chat.postMessage: %w", err)
		}
		var r slackReply
		if err := json.Unmarshal(body, &r); err != nil {
			return "", fmt.Errorf("slack chat.postMessage: http %d: decode reply: %w", status, err)
		}
		// Slack reports API errors with HTTP 200 and ok:false.
		if !r.OK {
			return "", fmt.Errorf("slack chat.postMessage: %s", r.Error)
		}
		return r.TS, nil
	}
	parts := chunks(m.Summary, slackLimit)
	ts, err := post(parts[0], "")
	if err != nil {
		return err
	}
	for _, text := range append(parts[1:], chunks(m.Detail, slackLimit)...) {
		if _, err := post(text, ts); err != nil {
			return err
		}
	}
	return nil
}

// Telegram sends through the Bot API. The detail is sent as replies to the
// summary, silently; the summary notifies only when the digest is loud.
type Telegram struct {
	Token  string
	ChatID string
	// BaseURL defaults to https://api.telegram.org.
	BaseURL string
	HTTP    *http.Client
}

type telegramReply struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

// Deliver implements Deliverer.
func (t *Telegram) Deliver(ctx context.Context, m Message) error {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	endpoint := base + "/bot" + t.Token + "/sendMessage"
	send := func(text string, replyTo int64, silent bool) (int64, error) {
		// No parse_mode: SLO names and PromQL-ish text would need
		// escaping for both Markdown and HTML, and plain text cannot
		// fail to parse.
		payload := map[string]any{
			"chat_id":              t.ChatID,
			"text":                 text,
			"disable_notification": silent,
		}
		if replyTo != 0 {
			payload["reply_parameters"] = map[string]any{"message_id": replyTo}
		}
		body, status, err := postJSON(ctx, t.HTTP, endpoint, "", payload)
		if err != nil {
			return 0, fmt.Errorf("telegram sendMessage: %w", err)
		}
		var r telegramReply
		if err := json.Unmarshal(body, &r); err != nil {
			return 0, fmt.Errorf("telegram sendMessage: http %d: decode reply: %w", status, err)
		}
		if !r.OK {
			return 0, fmt.Errorf("telegram sendMessage: http %d: %s", status, r.Description)
		}
		return r.Result.MessageID, nil
	}
	parts := chunks(m.Summary, telegramLimit)
	id, err := send(parts[0], 0, !m.Loud)
	if err != nil {
		return err
	}
	for _, text := range append(parts[1:], chunks(m.Detail, telegramLimit)...) {
		if _, err := send(text, id, true); err != nil {
			return err
		}
	}
	return nil
}

// Writer prints the message, for --deliver stdout and dry runs.
type Writer struct{ W io.Writer }

// Deliver implements Deliverer.
func (w Writer) Deliver(_ context.Context, m Message) error {
	_, err := fmt.Fprintf(w.W, "%s\n\n%s\n", m.Summary, m.Detail)
	return err
}

// postJSON sends payload and returns the (bounded) reply body and status.
// Transport errors are stripped of the request URL: the Telegram URL holds
// the bot token and a Slack webhook URL is itself a secret, and net/http
// puts the full URL into its error text.
func postJSON(ctx context.Context, c *http.Client, endpoint, auth string, payload any) ([]byte, int, error) {
	if c == nil {
		c = httpClient()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, redact(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, redact(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return nil, resp.StatusCode, redact(err)
	}
	return body, resp.StatusCode, nil
}

func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

// chunks splits text into pieces of at most limit characters, breaking on
// line boundaries where it can. It always returns at least one piece.
func chunks(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			out = append(out, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
			curLen = 0
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		n := utf8.RuneCountInString(line)
		if curLen+n > limit {
			flush()
		}
		// A single line longer than the limit is cut by characters.
		for n > limit {
			r := []rune(line)
			out = append(out, string(r[:limit]))
			line = string(r[limit:])
			n -= limit
		}
		cur.WriteString(line)
		curLen += n
	}
	flush()
	return out
}
