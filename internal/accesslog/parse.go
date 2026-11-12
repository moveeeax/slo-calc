// Package accesslog derives availability and latency SLIs from HTTP access
// logs, for services that have no request metrics yet. It parses the common
// log formats, normalises request paths into route templates, and
// aggregates per route and method.
package accesslog

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Format names a supported log layout.
type Format string

const (
	// Combined is the nginx/Apache "combined" format. An optional request
	// duration may follow the user agent as the last field (nginx
	// $request_time, or rt=0.123).
	Combined Format = "combined"
	// Common is the Common Log Format (Apache "common"): combined without
	// the referer and user agent. The optional trailing duration applies
	// here too.
	Common Format = "common"
	// Envoy is Envoy's default access log format.
	Envoy Format = "envoy"
	// JSON is one JSON object per line, with a user-supplied field mapping.
	JSON Format = "json"
)

// Record is one parsed request.
type Record struct {
	Time    time.Time
	HasTime bool
	Method  string
	// Path is the raw request target, query string included.
	Path      string
	Status    int
	UserAgent string
	// Duration is the request duration; HasDuration is false when the log
	// line carries none.
	Duration    time.Duration
	HasDuration bool
	// Cancelled marks requests the client gave up on before a response was
	// sent: nginx's 499, or Envoy's DC (downstream connection termination)
	// response flag.
	Cancelled bool
}

// Fields maps the logical record fields to keys of a JSON log line. Nested
// keys are written with dots ("http.status").
type Fields struct {
	Time      string
	Method    string
	Path      string
	Status    string
	Duration  string
	UserAgent string
	// Request is a combined "GET /path HTTP/1.1" request line, used when
	// Method or Path are absent from the line.
	Request string
}

// DefaultFields are the JSON keys used unless --field overrides them.
var DefaultFields = Fields{
	Time:      "time",
	Method:    "method",
	Path:      "path",
	Status:    "status",
	Duration:  "duration",
	UserAgent: "user_agent",
	Request:   "request",
}

// SetField overrides one key of the mapping from a "name=key" pair.
func (f *Fields) SetField(pair string) error {
	name, key, ok := strings.Cut(pair, "=")
	if !ok || key == "" {
		return fmt.Errorf("invalid field mapping %q (want name=key)", pair)
	}
	switch name {
	case "time":
		f.Time = key
	case "method":
		f.Method = key
	case "path":
		f.Path = key
	case "status":
		f.Status = key
	case "duration":
		f.Duration = key
	case "user_agent":
		f.UserAgent = key
	case "request":
		f.Request = key
	default:
		return fmt.Errorf("unknown field %q in mapping %q (want time, method, path, status, duration, user_agent or request)", name, pair)
	}
	return nil
}

// Parser turns log lines into records.
type Parser struct {
	Format Format
	// DurationUnit is the unit of a bare numeric duration: seconds for
	// nginx $request_time, microseconds for Apache %D, milliseconds for
	// Envoy %DURATION%.
	DurationUnit time.Duration
	Fields       Fields
}

// NewParser returns a parser for format with that format's usual duration
// unit. unit overrides it when non-zero.
func NewParser(format Format, unit time.Duration) (*Parser, error) {
	p := &Parser{Format: format, DurationUnit: unit, Fields: DefaultFields}
	switch format {
	case Combined, Common, JSON:
		if p.DurationUnit == 0 {
			p.DurationUnit = time.Second
		}
	case Envoy:
		if p.DurationUnit == 0 {
			p.DurationUnit = time.Millisecond
		}
	default:
		return nil, fmt.Errorf("unknown log format %q (want combined, common, envoy or json)", format)
	}
	return p, nil
}

// ParseUnit reads a --duration-unit value.
func ParseUnit(s string) (time.Duration, error) {
	switch s {
	case "":
		return 0, nil
	case "s":
		return time.Second, nil
	case "ms":
		return time.Millisecond, nil
	case "us":
		return time.Microsecond, nil
	case "ns":
		return time.Nanosecond, nil
	}
	return 0, fmt.Errorf("unknown duration unit %q (want s, ms, us or ns)", s)
}

// clfLine matches both combined and common lines. The referer/user-agent
// pair is optional so the same expression covers CLF; everything after it
// is kept for the optional duration field.
var clfLine = regexp.MustCompile(`^(\S+) \S+ (\S+) \[([^\]]+)\] "((?:[^"\\]|\\.)*)" (\d{3}) (\S+)(?: "((?:[^"\\]|\\.)*)" "((?:[^"\\]|\\.)*)")?(.*)$`)

// envoyLine matches the head of Envoy's default format:
//
//	[%START_TIME%] "%REQ(:METHOD)% %REQ(X-ENVOY-ORIGINAL-PATH?:PATH)% %PROTOCOL%"
//	%RESPONSE_CODE% %RESPONSE_FLAGS% %BYTES_RECEIVED% %BYTES_SENT% %DURATION%
//	%RESP(X-ENVOY-UPSTREAM-SERVICE-TIME)% "%REQ(X-FORWARDED-FOR)%" "%REQ(USER-AGENT)%" ...
//
// Fields after the user agent are ignored.
var envoyLine = regexp.MustCompile(`^\[([^\]]+)\] "([^"]*)" (\d+) (\S+) (\S+) (\S+) (\S+) (\S+) "([^"]*)" "([^"]*)"`)

const clfTime = "02/Jan/2006:15:04:05 -0700"

// Parse reads one line. Blank lines and lines that do not match the format
// return an error; callers count those as unparsable.
func (p *Parser) Parse(line string) (Record, error) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return Record{}, fmt.Errorf("empty line")
	}
	switch p.Format {
	case Combined, Common:
		return p.parseCLF(line)
	case Envoy:
		return p.parseEnvoy(line)
	case JSON:
		return p.parseJSON(line)
	}
	return Record{}, fmt.Errorf("unknown log format %q", p.Format)
}

func (p *Parser) parseCLF(line string) (Record, error) {
	m := clfLine.FindStringSubmatch(line)
	if m == nil {
		return Record{}, fmt.Errorf("line does not match the %s format", p.Format)
	}
	var r Record
	if t, err := time.Parse(clfTime, m[3]); err == nil {
		r.Time, r.HasTime = t, true
	}
	r.Method, r.Path = splitRequest(unescape(m[4]))
	r.Status, _ = strconv.Atoi(m[5])
	r.UserAgent = unescape(m[8])
	if d, ok := p.trailingDuration(m[9]); ok {
		r.Duration, r.HasDuration = d, true
	}
	r.Cancelled = r.Status == 499
	return r, nil
}

// trailingDuration picks the request duration out of whatever follows the
// standard fields: the last token, either a bare number or key=number
// (nginx configs often append "rt=$request_time").
func (p *Parser) trailingDuration(rest string) (time.Duration, bool) {
	toks := strings.Fields(rest)
	if len(toks) == 0 {
		return 0, false
	}
	last := strings.Trim(toks[len(toks)-1], `"`)
	if _, v, ok := strings.Cut(last, "="); ok {
		last = v
	}
	return p.toDuration(last)
}

func (p *Parser) toDuration(s string) (time.Duration, bool) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return time.Duration(math.Round(v * float64(p.DurationUnit))), true
}

func (p *Parser) parseEnvoy(line string) (Record, error) {
	m := envoyLine.FindStringSubmatch(line)
	if m == nil {
		return Record{}, fmt.Errorf("line does not match the envoy format")
	}
	var r Record
	if t, err := time.Parse(time.RFC3339Nano, m[1]); err == nil {
		r.Time, r.HasTime = t, true
	}
	r.Method, r.Path = splitRequest(m[2])
	r.Status, _ = strconv.Atoi(m[3])
	for _, flag := range strings.Split(m[4], ",") {
		if flag == "DC" {
			r.Cancelled = true
		}
	}
	if d, ok := p.toDuration(m[7]); ok {
		r.Duration, r.HasDuration = d, true
	}
	if m[10] != "-" {
		r.UserAgent = m[10]
	}
	return r, nil
}

func (p *Parser) parseJSON(line string) (Record, error) {
	dec := json.NewDecoder(strings.NewReader(line))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return Record{}, fmt.Errorf("invalid JSON: %w", err)
	}
	f := p.Fields
	var r Record
	status, ok := lookup(obj, f.Status)
	if !ok {
		return Record{}, fmt.Errorf("JSON line has no %q field", f.Status)
	}
	n, err := strconv.ParseFloat(status, 64)
	if err != nil {
		return Record{}, fmt.Errorf("JSON field %q is not a status code: %q", f.Status, status)
	}
	r.Status = int(n)

	r.Method, _ = lookup(obj, f.Method)
	r.Path, _ = lookup(obj, f.Path)
	if r.Method == "" || r.Path == "" {
		if req, ok := lookup(obj, f.Request); ok {
			m, path := splitRequest(req)
			if r.Method == "" {
				r.Method = m
			}
			if r.Path == "" {
				r.Path = path
			}
		}
	}
	if r.Path == "" {
		r.Path = "-"
	}
	r.UserAgent, _ = lookup(obj, f.UserAgent)
	if s, ok := lookup(obj, f.Duration); ok {
		if d, ok := p.toDuration(s); ok {
			r.Duration, r.HasDuration = d, true
		}
	}
	if s, ok := lookup(obj, f.Time); ok {
		r.Time, r.HasTime = parseJSONTime(s)
	}
	r.Cancelled = r.Status == 499
	return r, nil
}

// lookup walks a dotted key through nested objects and returns the value as
// a string. Numbers keep their literal text thanks to UseNumber.
func lookup(obj map[string]any, key string) (string, bool) {
	if key == "" {
		return "", false
	}
	// An exact top-level match wins over dotted traversal, so a flat key
	// such as "http.status" (as some shippers write it) still resolves.
	var v any = obj
	if direct, ok := obj[key]; ok {
		v = direct
	} else {
		for _, part := range strings.Split(key, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				return "", false
			}
			if v, ok = m[part]; !ok {
				return "", false
			}
		}
	}
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// parseJSONTime accepts RFC3339 strings, nginx's $time_local, and Unix
// timestamps in seconds (fractions allowed) or milliseconds.
func parseJSONTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(clfTime, s); err == nil {
		return t, true
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return time.Time{}, false
	}
	// Anything past 1e11 is too late to be seconds (year 5138), so it is
	// read as milliseconds.
	if v > 1e11 {
		v /= 1000
	}
	sec, frac := math.Modf(v)
	return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
}

// splitRequest breaks "GET /path HTTP/1.1" into method and target. A
// malformed request line (nginx logs "-" or raw bytes for those) yields an
// empty method and the "-" path.
func splitRequest(req string) (method, path string) {
	parts := strings.Fields(req)
	if len(parts) < 2 {
		return "", "-"
	}
	return parts[0], parts[1]
}

// unescape undoes nginx's \" and \\ escaping inside quoted fields.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}
