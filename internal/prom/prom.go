// Package prom is a tiny client for the Prometheus instant-query API,
// reduced to the single operation slo-calc needs: evaluate a PromQL
// expression at an instant and return one scalar value.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Querier evaluates a PromQL expression at a point in time and returns a
// single scalar. It is an interface so the evaluator can be tested with a
// fake and run for real against Prometheus.
type Querier interface {
	Query(ctx context.Context, expr string, ts time.Time) (float64, error)
}

// Client talks to a Prometheus HTTP API endpoint.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New builds a client for baseURL (e.g. http://localhost:9090).
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type apiResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// Query runs an instant query and collapses the result to one float. Scalar
// results return directly; a vector must contain exactly one sample.
func (c *Client) Query(ctx context.Context, expr string, ts time.Time) (float64, error) {
	q := url.Values{}
	q.Set("query", expr)
	q.Set("time", strconv.FormatInt(ts.Unix(), 10))
	u := c.BaseURL + "/api/v1/query?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var ar apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return 0, fmt.Errorf("decode prometheus response: %w", err)
	}
	if ar.Status != "success" {
		return 0, fmt.Errorf("prometheus error: %s", ar.Error)
	}
	return ParseResult(ar.Data.ResultType, ar.Data.Result)
}

// ParseResult reduces a Prometheus result payload to a single scalar. It is
// exported so it can be unit-tested against captured API fixtures.
func ParseResult(resultType string, raw json.RawMessage) (float64, error) {
	switch resultType {
	case "scalar":
		var s [2]json.RawMessage
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, err
		}
		return sampleValue(s[1])
	case "vector":
		var samples []struct {
			Value [2]json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(raw, &samples); err != nil {
			return 0, err
		}
		if len(samples) == 0 {
			return 0, nil // absent series => no observed events
		}
		if len(samples) > 1 {
			return 0, fmt.Errorf("query returned %d series, expected 1 (add an aggregation)", len(samples))
		}
		return sampleValue(samples[0].Value[1])
	default:
		return 0, fmt.Errorf("unsupported result type %q", resultType)
	}
}

func sampleValue(raw json.RawMessage) (float64, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	switch s {
	case "NaN":
		return 0, nil
	case "+Inf", "Inf":
		return 0, fmt.Errorf("value is +Inf")
	case "-Inf":
		return 0, fmt.Errorf("value is -Inf")
	}
	return strconv.ParseFloat(s, 64)
}
