package accesslog

import (
	"fmt"
	"regexp"
	"strings"
)

// Router collapses raw request paths into route templates, so that
// /orders/8213 and /orders/8214 count towards one /orders/:id SLI instead
// of becoming two series each.
type Router struct {
	templates [][]string
}

// AddTemplate registers an explicit route template such as
// /orders/:id/items. A segment starting with ":" or equal to "*" matches any
// single path segment. Templates are tried in the order added, before the
// automatic rules.
func (rt *Router) AddTemplate(t string) error {
	if !strings.HasPrefix(t, "/") {
		return fmt.Errorf("route template %q must start with /", t)
	}
	rt.templates = append(rt.templates, strings.Split(t, "/"))
	return nil
}

var (
	uuidSeg = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexSeg  = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	// tokenSeg is the alphabet of opaque ids (base62, base64url, slugs with
	// a numeric suffix such as order-82131234). Dots are left out so file
	// names like favicon-32x32.png stay as they are.
	tokenSeg = regexp.MustCompile(`^[A-Za-z0-9_~-]+$`)
)

// Route returns the template for a raw request target. The query string
// and fragment are dropped first.
func (rt *Router) Route(path string) string {
	if path == "" || path == "-" {
		return "-"
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	// Absolute-form targets ("GET http://host/x HTTP/1.1", proxies) keep
	// only their path.
	if i := strings.Index(path, "://"); i >= 0 {
		rest := path[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			path = rest[j:]
		} else {
			path = "/"
		}
	}
	if !strings.HasPrefix(path, "/") {
		return "-"
	}
	segs := strings.Split(path, "/")
	for _, t := range rt.templates {
		if matchTemplate(t, segs) {
			return strings.Join(t, "/")
		}
	}
	for i, s := range segs {
		segs[i] = normaliseSegment(s)
	}
	return strings.Join(segs, "/")
}

func matchTemplate(t, segs []string) bool {
	if len(t) != len(segs) {
		return false
	}
	for i := range t {
		if t[i] == "*" || (strings.HasPrefix(t[i], ":") && segs[i] != "") {
			continue
		}
		if t[i] != segs[i] {
			return false
		}
	}
	return true
}

// normaliseSegment replaces one path segment with a placeholder when it
// looks like an identifier rather than a fixed part of the route.
func normaliseSegment(s string) string {
	switch {
	case s == "":
		return s
	case isDigits(s):
		return ":id"
	case uuidSeg.MatchString(s):
		return ":uuid"
	case hexSeg.MatchString(s):
		return ":hash"
	case len(s) >= 8 && tokenSeg.MatchString(s) && hasDigit(s):
		return ":id"
	}
	return s
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func hasDigit(s string) bool {
	return strings.ContainsAny(s, "0123456789")
}
