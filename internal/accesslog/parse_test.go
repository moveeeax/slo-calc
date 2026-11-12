package accesslog

import (
	"testing"
	"time"
)

func mustParser(t *testing.T, f Format, unit time.Duration) *Parser {
	t.Helper()
	p, err := NewParser(f, unit)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseCombined(t *testing.T) {
	p := mustParser(t, Combined, 0)
	r, err := p.Parse(`10.0.0.1 - alice [01/Nov/2026:10:00:00 +0100] "GET /orders/8213?full=1 HTTP/1.1" 503 12 "https://ref/" "Mozilla/5.0 (X11)" 0.250`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "GET" || r.Path != "/orders/8213?full=1" || r.Status != 503 {
		t.Errorf("request fields: %+v", r)
	}
	if !r.HasTime || !r.Time.Equal(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("time = %v (has %v)", r.Time, r.HasTime)
	}
	if r.UserAgent != "Mozilla/5.0 (X11)" {
		t.Errorf("user agent = %q", r.UserAgent)
	}
	if !r.HasDuration || r.Duration != 250*time.Millisecond {
		t.Errorf("duration = %v (has %v)", r.Duration, r.HasDuration)
	}
}

func TestParseCombinedDurationVariants(t *testing.T) {
	const head = `1.2.3.4 - - [01/Nov/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 1 "-" "ua"`
	cases := []struct {
		line string
		unit time.Duration
		want time.Duration
		has  bool
	}{
		{head, 0, 0, false},
		{head + " rt=0.004", 0, 4 * time.Millisecond, true},
		{head + ` "0.5"`, 0, 500 * time.Millisecond, true},
		{head + " 1500", time.Microsecond, 1500 * time.Microsecond, true}, // Apache %D
		{head + " -", 0, 0, false},
	}
	for _, c := range cases {
		r, err := mustParser(t, Combined, c.unit).Parse(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if r.HasDuration != c.has || r.Duration != c.want {
			t.Errorf("%q: duration %v/%v, want %v/%v", c.line, r.Duration, r.HasDuration, c.want, c.has)
		}
	}
}

func TestParseCommonAndCancelled(t *testing.T) {
	r, err := mustParser(t, Common, 0).Parse(`127.0.0.1 - frank [10/Oct/2000:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 499 2326`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "/apache_pb.gif" || r.Status != 499 || !r.Cancelled || r.HasDuration {
		t.Errorf("common line: %+v", r)
	}
	// nginx writes "-" as the request for connections that never sent one.
	r, err = mustParser(t, Combined, 0).Parse(`1.2.3.4 - - [01/Nov/2026:10:00:00 +0000] "-" 400 0 "-" "-"`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "-" || r.Method != "" || r.Status != 400 {
		t.Errorf("empty request: %+v", r)
	}
}

func TestParseEnvoy(t *testing.T) {
	p := mustParser(t, Envoy, 0)
	r, err := p.Parse(`[2016-04-15T20:17:00.310Z] "POST /api/v1/locations HTTP/2" 204 - 154 0 226 100 "10.0.35.28" "nsq2http" "cc21d9b0-cf5c-432b-8c7e-98aeb7988cd2" "locations" "tcp://10.0.2.1:80"`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "POST" || r.Path != "/api/v1/locations" || r.Status != 204 || r.UserAgent != "nsq2http" {
		t.Errorf("envoy fields: %+v", r)
	}
	if r.Duration != 226*time.Millisecond || r.Cancelled {
		t.Errorf("envoy duration/cancel: %+v", r)
	}
	if !r.Time.Equal(time.Date(2016, 4, 15, 20, 17, 0, 310e6, time.UTC)) {
		t.Errorf("envoy time = %v", r.Time)
	}
	r, err = p.Parse(`[2016-04-15T20:17:00.310Z] "GET /slow HTTP/1.1" 0 UT,DC 0 0 5000 - "-" "-" "-" "-" "-"`)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Cancelled || r.Status != 0 || r.UserAgent != "" {
		t.Errorf("DC flag not detected: %+v", r)
	}
}

func TestParseJSON(t *testing.T) {
	p := mustParser(t, JSON, time.Millisecond)
	for _, pair := range []string{"status=resp.code", "duration=resp.ms", "time=ts"} {
		if err := p.Fields.SetField(pair); err != nil {
			t.Fatal(err)
		}
	}
	r, err := p.Parse(`{"ts":1793534400.5,"request":"PUT /items/9 HTTP/1.1","resp":{"code":"502","ms":12.5},"user_agent":"go-http"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != "PUT" || r.Path != "/items/9" || r.Status != 502 || r.UserAgent != "go-http" {
		t.Errorf("json fields: %+v", r)
	}
	if r.Duration != 12500*time.Microsecond {
		t.Errorf("json duration = %v", r.Duration)
	}
	if !r.Time.Equal(time.Unix(1793534400, 5e8)) {
		t.Errorf("json time = %v", r.Time)
	}

	// Defaults, RFC3339 time, numeric status.
	r, err = mustParser(t, JSON, 0).Parse(`{"time":"2026-11-01T00:00:00Z","method":"GET","path":"/x","status":200,"duration":0.1}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != 200 || r.Duration != 100*time.Millisecond || !r.HasTime {
		t.Errorf("default json: %+v", r)
	}

	for _, bad := range []string{`not json`, `{"path":"/x"}`, `{"status":"abc"}`} {
		if _, err := mustParser(t, JSON, 0).Parse(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}

func TestParserErrors(t *testing.T) {
	if _, err := NewParser("w3c", 0); err == nil {
		t.Error("unknown format should be rejected")
	}
	if _, err := ParseUnit("min"); err == nil {
		t.Error("unknown unit should be rejected")
	}
	var f Fields
	for _, bad := range []string{"status", "status=", "color=x"} {
		if err := f.SetField(bad); err == nil {
			t.Errorf("field mapping %q should be rejected", bad)
		}
	}
	p := mustParser(t, Combined, 0)
	for _, bad := range []string{"", "   ", "garbage line", `[2016-04-15T20:17:00.310Z] "GET / HTTP/1.1" 200`} {
		if _, err := p.Parse(bad); err == nil {
			t.Errorf("%q should not parse as combined", bad)
		}
	}
}
