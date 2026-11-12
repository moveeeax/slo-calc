package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// recorder is a fake chat API that keeps every JSON body it receives.
type recorder struct {
	mu     sync.Mutex
	paths  []string
	auth   []string
	bodies []map[string]any
	reply  func(n int) (int, string)
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.bodies = append(r.bodies, m)
	n := len(r.bodies)
	r.mu.Unlock()
	status, body := r.reply(n)
	w.WriteHeader(status)
	io.WriteString(w, body)
}

var msg = Message{Summary: "summary line", Detail: "detail line", Loud: true}

func TestSlackWebhook(t *testing.T) {
	rec := &recorder{reply: func(int) (int, string) { return 200, "ok" }}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	if err := (&SlackWebhook{URL: srv.URL + "/services/T/B/x"}).Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 2 || rec.bodies[0]["text"] != "summary line" || rec.bodies[1]["text"] != "detail line" {
		t.Errorf("webhook posts: %v", rec.bodies)
	}

	rec.reply = func(int) (int, string) { return 400, "invalid_payload" }
	err := (&SlackWebhook{URL: srv.URL}).Deliver(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "invalid_payload") {
		t.Errorf("webhook error = %v", err)
	}
}

func TestSlackBotThreads(t *testing.T) {
	rec := &recorder{reply: func(int) (int, string) { return 200, `{"ok":true,"ts":"1700000000.000100"}` }}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	bot := &SlackBot{Token: "xoxb-1", Channel: "C123", BaseURL: srv.URL}
	if err := bot.Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("want summary + one thread reply, got %v", rec.bodies)
	}
	if rec.paths[0] != "/chat.postMessage" || rec.auth[0] != "Bearer xoxb-1" {
		t.Errorf("request: %s %s", rec.paths[0], rec.auth[0])
	}
	if _, ok := rec.bodies[0]["thread_ts"]; ok || rec.bodies[0]["channel"] != "C123" {
		t.Errorf("summary: %v", rec.bodies[0])
	}
	if rec.bodies[1]["thread_ts"] != "1700000000.000100" || rec.bodies[1]["text"] != "detail line" {
		t.Errorf("detail should reply in the summary's thread: %v", rec.bodies[1])
	}

	rec.reply = func(int) (int, string) { return 200, `{"ok":false,"error":"channel_not_found"}` }
	if err := bot.Deliver(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("ok:false must be an error, got %v", err)
	}
}

func TestTelegramRepliesAndSilence(t *testing.T) {
	rec := &recorder{reply: func(n int) (int, string) {
		return 200, fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, 40+n)
	}}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	tg := &Telegram{Token: "123:abc", ChatID: "-100200", BaseURL: srv.URL}
	if err := tg.Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if rec.paths[0] != "/bot123:abc/sendMessage" {
		t.Errorf("path = %s", rec.paths[0])
	}
	sum, det := rec.bodies[0], rec.bodies[1]
	if sum["chat_id"] != "-100200" || sum["disable_notification"] != false || sum["reply_parameters"] != nil {
		t.Errorf("loud summary: %v", sum)
	}
	rp, _ := det["reply_parameters"].(map[string]any)
	if rp["message_id"] != float64(41) || det["disable_notification"] != true {
		t.Errorf("detail should silently reply to message 41: %v", det)
	}

	quiet := msg
	quiet.Loud = false
	rec.bodies = nil
	if err := tg.Deliver(context.Background(), quiet); err != nil {
		t.Fatal(err)
	}
	if rec.bodies[0]["disable_notification"] != true {
		t.Errorf("a digest without a fast burn should not notify: %v", rec.bodies[0])
	}

	rec.reply = func(int) (int, string) { return 400, `{"ok":false,"description":"Bad Request: chat not found"}` }
	if err := tg.Deliver(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("telegram error = %v", err)
	}
}

// TestTokenNotInErrors guards against net/http's habit of quoting the full
// request URL in transport errors: for Telegram that URL holds the token.
func TestTokenNotInErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // connection refused from here on
	err := (&Telegram{Token: "123:SECRET", ChatID: "1", BaseURL: base}).Deliver(context.Background(), msg)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks the token: %v", err)
	}
	err = (&SlackWebhook{URL: base + "/services/T/B/SECRET"}).Deliver(context.Background(), msg)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("webhook error leaks the URL: %v", err)
	}
}

func TestChunks(t *testing.T) {
	if got := chunks("short", 10); len(got) != 1 || got[0] != "short" {
		t.Errorf("short text: %q", got)
	}
	text := strings.Repeat("ab\n", 10) // 30 chars
	got := chunks(text, 7)
	for _, c := range got {
		if utf8.RuneCountInString(c) > 7 {
			t.Errorf("chunk over limit: %q", c)
		}
	}
	if strings.Join(got, "\n") != strings.TrimRight(text, "\n") {
		t.Errorf("chunks lost text: %q", got)
	}
	long := strings.Repeat("я", 25)
	got = chunks(long, 10)
	if len(got) != 3 || utf8.RuneCountInString(got[0]) != 10 || strings.Join(got, "") != long {
		t.Errorf("long line split by runes: %q", got)
	}
}

func TestWriter(t *testing.T) {
	var b bytes.Buffer
	if err := (Writer{W: &b}).Deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if b.String() != "summary line\n\ndetail line\n" {
		t.Errorf("writer output %q", b.String())
	}
}
