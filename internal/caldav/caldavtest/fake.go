// Package caldavtest is a small in-memory CalDAV server for tests: principal
// discovery (with a /.well-known/caldav redirect), calendar listing,
// sync-collection, calendar-multiget, calendar-query, GET, PUT and DELETE
// with If-Match / If-None-Match.
package caldavtest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Server is the fake. Lock Mu when reading its maps from a test.
type Server struct {
	*httptest.Server
	Mu        sync.Mutex
	User      string
	Pass      string
	Bearer    string // accepted instead of Basic when set
	Root      string // path prefix of everything (e.g. "/remote.php/dav")
	NoSync    bool   // refuse sync-collection like an old server
	Calendars map[string]*Calendar
	Requests  []Request
	seq       int
}

// Calendar is one collection.
type Calendar struct {
	Name     string // path segment
	Display  string
	Color    string
	Comps    []string
	ReadOnly bool
	Objects  map[string]*Object
	gone     map[string]int // object name → seq of its delete
	seq      int
}

// Object is one stored resource.
type Object struct {
	Data string
	ETag string
	seq  int
}

// Request is what the fake saw (for assertions).
type Request struct {
	Method      string
	Path        string
	IfMatch     string
	IfNoneMatch string
	Kind        string // REPORT kind: sync-collection, calendar-multiget, calendar-query
	Auth        string
}

// New starts a fake with user/pass and no calendars.
func New(user, pass string) *Server {
	f := &Server{User: user, Pass: pass, Calendars: map[string]*Calendar{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *Server) principal() string { return f.Root + "/principals/" + f.User + "/" }
func (f *Server) home() string      { return f.Root + "/calendars/" + f.User + "/" }

// CalendarHref is the path of a calendar.
func (f *Server) CalendarHref(name string) string { return f.home() + name + "/" }

// AddCalendar adds a collection.
func (f *Server) AddCalendar(name, display, color string, readOnly bool, comps ...string) *Calendar {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if len(comps) == 0 {
		comps = []string{"VEVENT"}
	}
	f.seq++
	c := &Calendar{Name: name, Display: display, Color: color, Comps: comps, ReadOnly: readOnly, Objects: map[string]*Object{}, gone: map[string]int{}, seq: f.seq}
	f.Calendars[name] = c
	return c
}

// RemoveCalendar drops a collection.
func (f *Server) RemoveCalendar(name string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	delete(f.Calendars, name)
}

func etagOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// PutObject stores a resource as if another client wrote it.
func (f *Server) PutObject(cal, name, data string) *Object {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.put(f.Calendars[cal], name, data)
}

func (f *Server) put(c *Calendar, name, data string) *Object {
	f.seq++
	o := &Object{Data: data, ETag: etagOf(data + strconv.Itoa(f.seq)), seq: f.seq}
	c.Objects[name] = o
	delete(c.gone, name)
	c.seq = f.seq
	return o
}

// DeleteObject removes a resource as if another client did.
func (f *Server) DeleteObject(cal, name string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.del(f.Calendars[cal], name)
}

func (f *Server) del(c *Calendar, name string) {
	f.seq++
	delete(c.Objects, name)
	c.gone[name] = f.seq
	c.seq = f.seq
}

// Object returns a stored resource (nil when missing).
func (f *Server) Object(cal, name string) *Object {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if c := f.Calendars[cal]; c != nil {
		return c.Objects[name]
	}
	return nil
}

// Count returns how many requests matched method (and REPORT kind when given).
func (f *Server) Count(method, kind string) int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	n := 0
	for _, r := range f.Requests {
		if r.Method == method && (kind == "" || r.Kind == kind) {
			n++
		}
	}
	return n
}

// Last returns the last request with that method.
func (f *Server) Last(method string) *Request {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	for i := len(f.Requests) - 1; i >= 0; i-- {
		if f.Requests[i].Method == method {
			r := f.Requests[i]
			return &r
		}
	}
	return nil
}

func (f *Server) authorized(r *http.Request) bool {
	if f.Bearer != "" && r.Header.Get("Authorization") == "Bearer "+f.Bearer {
		return true
	}
	u, p, ok := r.BasicAuth()
	return ok && f.User != "" && u == f.User && p == f.Pass
}

var hrefRe = regexp.MustCompile(`<d:href>([^<]*)</d:href>`)
var tokenRe = regexp.MustCompile(`<d:sync-token>([^<]*)</d:sync-token>`)

func (f *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b := string(body)
	f.Mu.Lock()
	defer f.Mu.Unlock()
	req := Request{Method: r.Method, Path: r.URL.Path, IfMatch: r.Header.Get("If-Match"), IfNoneMatch: r.Header.Get("If-None-Match"), Auth: r.Header.Get("Authorization")}
	switch {
	case strings.Contains(b, "sync-collection"):
		req.Kind = "sync-collection"
	case strings.Contains(b, "calendar-multiget"):
		req.Kind = "calendar-multiget"
	case strings.Contains(b, "calendar-query"):
		req.Kind = "calendar-query"
	}
	f.Requests = append(f.Requests, req)

	if r.URL.Path == "/.well-known/caldav" || r.URL.Path == "/.well-known/caldav/" {
		http.Redirect(w, r, f.Root+"/", http.StatusMovedPermanently)
		return
	}
	if !f.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	p := r.URL.Path
	switch r.Method {
	case "PROPFIND":
		f.propfind(w, r, p, b)
	case "REPORT":
		f.report(w, r, p, b, req.Kind)
	case http.MethodGet, http.MethodPut, http.MethodDelete:
		c, name := f.locate(p)
		if c == nil || name == "" {
			http.NotFound(w, r)
			return
		}
		f.object(w, r, c, name, b)
	default:
		http.Error(w, "no", http.StatusMethodNotAllowed)
	}
}

// locate maps a path to a calendar and an object name.
func (f *Server) locate(p string) (*Calendar, string) {
	if !strings.HasPrefix(p, f.home()) {
		return nil, ""
	}
	rest := strings.TrimPrefix(p, f.home())
	parts := strings.SplitN(rest, "/", 2)
	c := f.Calendars[parts[0]]
	if c == nil {
		return nil, ""
	}
	if len(parts) == 1 {
		return c, ""
	}
	return c, parts[1]
}

func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func hrefEsc(p string) string { return esc((&url.URL{Path: p}).EscapedPath()) }

const msOpen = `<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/" xmlns:x1="http://apple.com/ns/ical/">`

func okProp(href, props string) string {
	return `<d:response><d:href>` + hrefEsc(href) + `</d:href><d:propstat><d:prop>` + props + `</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`
}

func (f *Server) token(n int) string { return "http://fake.example/sync/" + strconv.Itoa(n) }

func (f *Server) calProps(c *Calendar) string {
	var comps strings.Builder
	for _, cp := range c.Comps {
		comps.WriteString(`<c:comp name="` + cp + `"/>`)
	}
	priv := `<d:privilege><d:read/></d:privilege>`
	if !c.ReadOnly {
		priv += `<d:privilege><d:write/></d:privilege>`
	}
	return `<d:displayname>` + esc(c.Display) + `</d:displayname><d:resourcetype><d:collection/><c:calendar/></d:resourcetype>` +
		`<c:supported-calendar-component-set>` + comps.String() + `</c:supported-calendar-component-set>` +
		`<d:current-user-privilege-set>` + priv + `</d:current-user-privilege-set>` +
		`<x1:calendar-color>` + esc(c.Color) + `</x1:calendar-color><cs:getctag>ctag-` + strconv.Itoa(c.seq) + `</cs:getctag>` +
		`<d:sync-token>` + f.token(f.seq) + `</d:sync-token>`
}

func (f *Server) propfind(w http.ResponseWriter, r *http.Request, p, body string) {
	depth := r.Header.Get("Depth")
	var out strings.Builder
	out.WriteString(msOpen)
	switch {
	case p == f.Root+"/" || p == f.Root || p == "/" || p == f.principal():
		props := `<d:current-user-principal><d:href>` + hrefEsc(f.principal()) + `</d:href></d:current-user-principal>`
		if p == f.principal() {
			props += `<c:calendar-home-set><d:href>` + hrefEsc(f.home()) + `</d:href></c:calendar-home-set>`
		}
		out.WriteString(okProp(p, props))
	case p == f.home():
		out.WriteString(okProp(p, `<d:resourcetype><d:collection/></d:resourcetype>`))
		if depth == "1" {
			names := make([]string, 0, len(f.Calendars))
			for n := range f.Calendars {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				out.WriteString(okProp(f.CalendarHref(n), f.calProps(f.Calendars[n])))
			}
		}
	default:
		c, name := f.locate(p)
		if c == nil {
			http.NotFound(w, r)
			return
		}
		if name != "" {
			o := c.Objects[name]
			if o == nil {
				http.NotFound(w, r)
				return
			}
			out.WriteString(okProp(p, `<d:getetag>`+esc(o.ETag)+`</d:getetag><d:resourcetype/>`))
			break
		}
		out.WriteString(okProp(f.CalendarHref(c.Name), f.calProps(c)))
		if depth == "1" {
			for _, n := range sortedNames(c) {
				out.WriteString(okProp(f.CalendarHref(c.Name)+n, `<d:getetag>`+esc(c.Objects[n].ETag)+`</d:getetag><d:resourcetype/>`))
			}
		}
	}
	out.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	_, _ = io.WriteString(w, out.String())
}

func sortedNames(c *Calendar) []string {
	names := make([]string, 0, len(c.Objects))
	for n := range c.Objects {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (f *Server) report(w http.ResponseWriter, r *http.Request, p, body, kind string) {
	c, name := f.locate(p)
	if c == nil || name != "" {
		http.NotFound(w, r)
		return
	}
	base := f.CalendarHref(c.Name)
	var out strings.Builder
	out.WriteString(msOpen)
	switch kind {
	case "sync-collection":
		if f.NoSync {
			http.Error(w, "not supported", http.StatusNotImplemented)
			return
		}
		since := 0
		if m := tokenRe.FindStringSubmatch(body); m != nil && m[1] != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(m[1], "http://fake.example/sync/"))
			if err != nil || n > f.seq {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `<?xml version="1.0"?><d:error xmlns:d="DAV:"><d:valid-sync-token/></d:error>`)
				return
			}
			since = n
		}
		for _, n := range sortedNames(c) {
			if o := c.Objects[n]; o.seq > since {
				out.WriteString(okProp(base+n, `<d:getetag>`+esc(o.ETag)+`</d:getetag>`))
			}
		}
		if since > 0 {
			for n, s := range c.gone {
				if s > since {
					out.WriteString(`<d:response><d:href>` + hrefEsc(base+n) + `</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`)
				}
			}
		}
		out.WriteString(`<d:sync-token>` + f.token(f.seq) + `</d:sync-token>`)
	case "calendar-multiget":
		for _, m := range hrefRe.FindAllStringSubmatch(body, -1) {
			h, _ := url.PathUnescape(m[1])
			oc, on := f.locate(h)
			if oc == nil || oc.Objects[on] == nil {
				out.WriteString(`<d:response><d:href>` + hrefEsc(h) + `</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`)
				continue
			}
			o := oc.Objects[on]
			out.WriteString(okProp(h, `<d:getetag>`+esc(o.ETag)+`</d:getetag><c:calendar-data>`+esc(o.Data)+`</c:calendar-data>`))
		}
	case "calendar-query":
		for _, n := range sortedNames(c) {
			if o := c.Objects[n]; strings.Contains(o.Data, "BEGIN:VEVENT") {
				out.WriteString(okProp(base+n, `<d:getetag>`+esc(o.ETag)+`</d:getetag>`))
			}
		}
	default:
		http.Error(w, "unknown report", http.StatusBadRequest)
		return
	}
	out.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	_, _ = io.WriteString(w, out.String())
}

func (f *Server) object(w http.ResponseWriter, r *http.Request, c *Calendar, name, body string) {
	o := c.Objects[name]
	switch r.Method {
	case http.MethodGet:
		if o == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", o.ETag)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		_, _ = io.WriteString(w, o.Data)
	case http.MethodPut:
		if c.ReadOnly {
			http.Error(w, "read-only", http.StatusForbidden)
			return
		}
		if im := r.Header.Get("If-Match"); im != "" && (o == nil || o.ETag != im) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if r.Header.Get("If-None-Match") == "*" && o != nil {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		created := o == nil
		n := f.put(c, name, body)
		w.Header().Set("ETag", n.ETag)
		if created {
			w.WriteHeader(http.StatusCreated)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	case http.MethodDelete:
		if o == nil {
			http.NotFound(w, r)
			return
		}
		if im := r.Header.Get("If-Match"); im != "" && o.ETag != im {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		f.del(c, name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// Event returns a minimal VCALENDAR with one VEVENT.
func Event(uid, summary, start, lastModified string) string {
	lm := ""
	if lastModified != "" {
		lm = "LAST-MODIFIED:" + lastModified + "\r\n"
	}
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//fake//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20261001T000000Z\r\n" + lm +
		"DTSTART:" + start + "\r\nDURATION:PT1H\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// Todo returns a minimal VCALENDAR with one VTODO.
func Todo(uid, summary string) string {
	return fmt.Sprintf("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//fake//EN\r\nBEGIN:VTODO\r\nUID:%s\r\nDTSTAMP:20261001T000000Z\r\nSUMMARY:%s\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", uid, summary)
}
