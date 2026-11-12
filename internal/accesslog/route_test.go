package accesslog

import "testing"

func TestRouteNormalisation(t *testing.T) {
	var rt Router
	cases := map[string]string{
		"/orders/8213":                                "/orders/:id",
		"/orders/8213/items?page=2":                   "/orders/:id/items",
		"/users/3F2504E0-4F89-11D3-9A0C-0305E82C3301": "/users/:uuid",
		"/blobs/9f86d081884c7d659a2feaa0c55ad015":     "/blobs/:hash",
		"/o/ord_8x7Ka9mQ2":                            "/o/:id",
		"/api/v2/health":                              "/api/v2/health",
		"/static/favicon-32x32.png":                   "/static/favicon-32x32.png",
		"/":                                           "/",
		"-":                                           "-",
		"*":                                           "-",
		"http://example.com/orders/1":                 "/orders/:id",
		"http://example.com":                          "/",
	}
	for in, want := range cases {
		if got := rt.Route(in); got != want {
			t.Errorf("Route(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRouteTemplates(t *testing.T) {
	var rt Router
	for _, tpl := range []string{"/repos/:owner/:repo", "/files/*"} {
		if err := rt.AddTemplate(tpl); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.AddTemplate("repos/:x"); err == nil {
		t.Error("a template without a leading slash should be rejected")
	}
	cases := map[string]string{
		"/repos/moveeeax/slo-calc": "/repos/:owner/:repo",
		"/repos/moveeeax":          "/repos/moveeeax",
		"/files/report.pdf":        "/files/*",
		"/repos//x":                "/repos//x",
	}
	for in, want := range cases {
		if got := rt.Route(in); got != want {
			t.Errorf("Route(%q) = %q, want %q", in, got, want)
		}
	}
}
