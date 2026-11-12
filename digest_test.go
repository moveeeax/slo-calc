package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// promServer answers every instant query with the same scalar, picked by
// whether the expression is a good or a total query.
func promServer(t *testing.T, good, total string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := total
		if strings.HasPrefix(r.URL.Query().Get("query"), "sum(increase(good") {
			v = good
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"success","data":{"resultType":"scalar","result":[0,"`+v+`"]}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	old := getenv
	getenv = func(k string) string { return env[k] }
	t.Cleanup(func() { getenv = old })
}

func TestDigestQuietByDefault(t *testing.T) {
	prom := promServer(t, "1000", "1000")
	sp := writeSpec(t, minimalSpec)
	stdout, stderr, err := runCLI(t, "digest", "--spec", sp, "--prometheus", prom.URL)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" || !strings.Contains(stderr, "not delivering") {
		t.Errorf("healthy digest should stay quiet; stdout=%q stderr=%q", stdout, stderr)
	}
	stdout, _, err = runCLI(t, "digest", "--spec", sp, "--prometheus", prom.URL, "--always")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "the SLO is healthy, nothing moved") || !strings.Contains(stdout, "api (objective 99.9%)") {
		t.Errorf("--always output:\n%s", stdout)
	}
}

func TestDigestFailOnBurn(t *testing.T) {
	// 2% errors in every window: fast burn at 20x, budget 20x overspent.
	prom := promServer(t, "980", "1000")
	sp := writeSpec(t, minimalSpec)
	stdout, _, err := runCLI(t, "digest", "--spec", sp, "--prometheus", prom.URL, "--fail-on-burn")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 || !strings.Contains(ee.Error(), "api") {
		t.Fatalf("want exit status 2 naming the SLO, got %v", err)
	}
	if !strings.Contains(stdout, "[FAST BURN] api") {
		t.Errorf("the digest is still delivered before failing:\n%s", stdout)
	}
	// Without the flag the same state exits 0.
	if _, _, err := runCLI(t, "digest", "--spec", sp, "--prometheus", prom.URL); err != nil {
		t.Errorf("without --fail-on-burn: %v", err)
	}
}

func TestDigestSlackWebhook(t *testing.T) {
	var mu sync.Mutex
	var posts []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, string(b))
		mu.Unlock()
		io.WriteString(w, "ok")
	}))
	defer hook.Close()
	withEnv(t, map[string]string{"SLACK_WEBHOOK_URL": hook.URL})
	prom := promServer(t, "980", "1000")
	stdout, _, err := runCLI(t, "digest", "--spec", writeSpec(t, minimalSpec), "--prometheus", prom.URL, "--deliver", "slack")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" || len(posts) != 2 || !strings.Contains(posts[0], "FAST BURN") {
		t.Errorf("stdout=%q posts=%q", stdout, posts)
	}
}

func TestDigestErrors(t *testing.T) {
	sp := writeSpec(t, minimalSpec)
	withEnv(t, map[string]string{})
	cases := map[string][]string{
		"no spec":            {"digest", "--prometheus", "http://x"},
		"no prometheus":      {"digest", "--spec", sp},
		"bad window":         {"digest", "--spec", sp, "--prometheus", "http://x", "--window", "0d"},
		"bad compare":        {"digest", "--spec", sp, "--prometheus", "http://x", "--compare", "yesterday"},
		"negative moved":     {"digest", "--spec", sp, "--prometheus", "http://x", "--moved", "-1"},
		"bad at":             {"digest", "--spec", sp, "--prometheus", "http://x", "--at", "now"},
		"unknown deliver":    {"digest", "--spec", sp, "--prometheus", "http://x", "--deliver", "email"},
		"slack no secret":    {"digest", "--spec", sp, "--prometheus", "http://x", "--deliver", "slack"},
		"slack bot no token": {"digest", "--spec", sp, "--prometheus", "http://x", "--deliver", "slack", "--slack-channel", "C1"},
		"telegram no token":  {"digest", "--spec", sp, "--prometheus", "http://x", "--deliver", "telegram", "--chat-id", "1"},
	}
	for label, args := range cases {
		if _, _, err := runCLI(t, args...); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
	withEnv(t, map[string]string{"TELEGRAM_BOT_TOKEN": "1:x"})
	if _, _, err := runCLI(t, "digest", "--spec", sp, "--prometheus", "http://x", "--deliver", "telegram"); err == nil ||
		!strings.Contains(err.Error(), "chat-id") {
		t.Errorf("telegram without a chat id: %v", err)
	}
}
