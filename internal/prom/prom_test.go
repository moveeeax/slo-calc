package prom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseResultScalar(t *testing.T) {
	v, err := ParseResult("scalar", json.RawMessage(`[1435781451.781,"42.5"]`))
	if err != nil {
		t.Fatal(err)
	}
	if v != 42.5 {
		t.Errorf("got %v, want 42.5", v)
	}
}

func TestParseResultVector(t *testing.T) {
	raw := json.RawMessage(`[{"metric":{},"value":[1435781451.781,"7"]}]`)
	v, err := ParseResult("vector", raw)
	if err != nil {
		t.Fatal(err)
	}
	if v != 7 {
		t.Errorf("got %v, want 7", v)
	}
}

func TestParseResultEmptyVector(t *testing.T) {
	v, err := ParseResult("vector", json.RawMessage(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("empty vector should be 0, got %v", v)
	}
}

func TestParseResultMultiSeriesErrors(t *testing.T) {
	raw := json.RawMessage(`[{"value":[1,"1"]},{"value":[1,"2"]}]`)
	if _, err := ParseResult("vector", raw); err == nil {
		t.Errorf("multi-series result should error")
	}
}

func TestParseResultNaN(t *testing.T) {
	v, err := ParseResult("scalar", json.RawMessage(`[1,"NaN"]`))
	if err != nil || v != 0 {
		t.Errorf("NaN should map to 0, got %v err %v", v, err)
	}
}

func TestClientQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got != "up" {
			t.Errorf("query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	v, err := c.Query(context.Background(), "up", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Errorf("got %v, want 1", v)
	}
}

func TestClientQueryError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).Query(context.Background(), "x", time.Unix(1, 0)); err == nil {
		t.Errorf("expected error from error status")
	}
}
