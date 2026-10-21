// Package caldav is a small CalDAV client: enough to find the calendars of
// an account on any CalDAV server (iCloud, Fastmail, Nextcloud, Radicale,
// Google…) and to read and write the events in them.
//
// Hrefs are kept in a canonical form: the decoded path when the resource
// lives on the client's own server (the usual case), or scheme://host plus
// the decoded path when it lives elsewhere (iCloud hands out a calendar home
// on another host). Every method takes and returns hrefs in that form.
package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Client talks to one server as one user.
type Client struct {
	Base  string // https://cloud.example.com or https://cloud.example.com/remote.php/dav
	User  string
	Pass  string
	Token func(ctx context.Context) (string, error) // set: Bearer auth instead of Basic
	HTTP  *http.Client
	UA    string
}

// ErrAuth is a 401.
var ErrAuth = errors.New("the server refused the sign-in")

// ErrInvalidToken says the sync token is no longer known; do a full listing.
var ErrInvalidToken = errors.New("sync token no longer valid")

// ErrPrecondition is a 412 on PUT/DELETE (someone else changed it).
var ErrPrecondition = errors.New("changed on the server meanwhile")

// ErrNotFound is a 404.
var ErrNotFound = errors.New("not found on the server")

// ErrNoCalendars says discovery found no calendar home at the address.
var ErrNoCalendars = errors.New("no CalDAV calendars found at this address")

// Calendar is one collection.
type Calendar struct {
	Href       string
	Name       string
	Color      string
	Components []string
	CTag       string
	SyncToken  string
	ReadOnly   bool
	Order      int
}

// Change is one resource as a listing or a sync reports it.
type Change struct {
	Href    string
	ETag    string
	Deleted bool
}

// Resource is one fetched .ics.
type Resource struct {
	Href string
	ETag string
	Data string
}

// New returns a client that signs in with Basic auth.
func New(base, user, pass string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), User: user, Pass: pass, HTTP: &http.Client{Timeout: 60 * time.Second}, UA: "PocketCalendar"}
}

// NewBearer returns a client that sends "Authorization: Bearer <token>",
// asking token for it before every request (so it can refresh an OAuth token).
func NewBearer(base string, token func(ctx context.Context) (string, error)) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}, UA: "PocketCalendar"}
}

// ---------- hrefs ----------

func (c *Client) baseURL() *url.URL {
	u, err := url.Parse(c.Base)
	if err != nil || u.Host == "" {
		return &url.URL{Scheme: "https", Host: "invalid"}
	}
	return u
}

// BasePath is the decoded path of the base address ("" for none).
func (c *Client) BasePath() string {
	p := c.baseURL().Path
	if p == "/" {
		return ""
	}
	return p
}

func hostKey(scheme, host string) string {
	h := strings.ToLower(host)
	if (scheme == "https" && strings.HasSuffix(h, ":443")) || (scheme == "http" && strings.HasSuffix(h, ":80")) {
		h = h[:strings.LastIndex(h, ":")]
	}
	return strings.ToLower(scheme) + "://" + h
}

func (c *Client) origin() string {
	b := c.baseURL()
	return hostKey(b.Scheme, b.Host)
}

// splitAbs splits a canonical absolute href into origin and decoded path.
func splitAbs(href string) (scheme, host, p string, ok bool) {
	i := strings.Index(href, "://")
	if i <= 0 || (href[:i] != "http" && href[:i] != "https") {
		return "", "", "", false
	}
	rest := href[i+3:]
	j := strings.Index(rest, "/")
	if j < 0 {
		return href[:i], rest, "/", true
	}
	return href[:i], rest[:j], rest[j:], true
}

// URL turns a canonical href into the address to request.
func (c *Client) URL(href string) string {
	if scheme, host, p, ok := splitAbs(href); ok {
		return (&url.URL{Scheme: scheme, Host: host, Path: p}).String()
	}
	b := c.baseURL()
	u := url.URL{Scheme: b.Scheme, Host: b.Host, User: nil}
	switch {
	case strings.HasPrefix(href, "/"):
		u.Path = href
	case href == "":
		u.Path = b.Path
	default:
		dir := b.Path
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		u.Path = path.Join(dir, href)
		if strings.HasSuffix(href, "/") {
			u.Path += "/"
		}
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

// canon turns an address the server gave into the canonical href.
func (c *Client) canon(u *url.URL) string {
	p := u.Path
	if p == "" {
		p = "/"
	}
	if u.Host == "" || hostKey(u.Scheme, u.Host) == c.origin() {
		return p
	}
	return hostKey(u.Scheme, u.Host) + p
}

// resolveHref reads an href of a multistatus relative to the request address.
func (c *Client) resolveHref(reqURL *url.URL, href string) string {
	href = strings.TrimSpace(href)
	u, err := reqURL.Parse(href)
	if err != nil {
		if p, err := url.PathUnescape(href); err == nil {
			return p
		}
		return href
	}
	return c.canon(u)
}

// escapedPath is the path of a canonical href as it goes into a request body.
func escapedPath(href string) string {
	p := href
	if _, _, ap, ok := splitAbs(href); ok {
		p = ap
	}
	return (&url.URL{Path: p}).EscapedPath()
}

// SameHref compares two canonical hrefs, ignoring a trailing slash.
func SameHref(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// ---------- requests ----------

func (c *Client) do(ctx context.Context, method, href string, headers map[string]string, body string) (int, http.Header, []byte, *url.URL, error) {
	target := c.URL(href)
	hc := http.Client{Timeout: 60 * time.Second}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	// WebDAV methods must not turn into GETs on a redirect: follow them here.
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
		if err != nil {
			return 0, nil, nil, nil, err
		}
		if c.Token != nil {
			tok, err := c.Token(ctx)
			if err != nil {
				return 0, nil, nil, nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		} else if c.User != "" || c.Pass != "" {
			req.SetBasicAuth(c.User, c.Pass)
		}
		req.Header.Set("User-Agent", c.UA)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := hc.Do(req)
		if err != nil {
			return 0, nil, nil, nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		res.Body.Close()
		switch res.StatusCode {
		case 301, 302, 303, 307, 308:
			loc := res.Header.Get("Location")
			if loc != "" && hop < 5 {
				next, err := req.URL.Parse(loc)
				// never follow to plain http from https (the sign-in goes along)
				if err == nil && (next.Scheme == "https" || req.URL.Scheme == "http") {
					target = next.String()
					continue
				}
			}
		case 401:
			return res.StatusCode, res.Header, b, req.URL, ErrAuth
		}
		return res.StatusCode, res.Header, b, req.URL, nil
	}
}

type xnode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Nodes   []xnode    `xml:",any"`
}

func (n *xnode) child(local string) *xnode {
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			return &n.Nodes[i]
		}
	}
	return nil
}

func (n *xnode) children(local string) []*xnode {
	var out []*xnode
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			out = append(out, &n.Nodes[i])
		}
	}
	return out
}

func (n *xnode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// find walks the tree for the first node with that local name.
func (n *xnode) find(local string) *xnode {
	if n.XMLName.Local == local {
		return n
	}
	for i := range n.Nodes {
		if f := n.Nodes[i].find(local); f != nil {
			return f
		}
	}
	return nil
}

type response struct {
	href   string
	status string
	props  map[string]*xnode // local name → node, from 200 propstats
}

func (c *Client) parseMultistatus(reqURL *url.URL, b []byte) ([]response, *xnode, error) {
	var root xnode
	if err := xml.Unmarshal(b, &root); err != nil {
		return nil, nil, fmt.Errorf("unexpected answer: %w", err)
	}
	var out []response
	for _, r := range root.children("response") {
		resp := response{props: map[string]*xnode{}}
		if h := r.child("href"); h != nil {
			resp.href = c.resolveHref(reqURL, h.Text)
		}
		if st := r.child("status"); st != nil {
			resp.status = strings.TrimSpace(st.Text)
		}
		for _, ps := range r.children("propstat") {
			st := ps.child("status")
			if st == nil || !strings.Contains(st.Text, " 200") {
				continue
			}
			if p := ps.child("prop"); p != nil {
				for i := range p.Nodes {
					resp.props[p.Nodes[i].XMLName.Local] = &p.Nodes[i]
				}
			}
		}
		out = append(out, resp)
	}
	return out, &root, nil
}

func (c *Client) hrefProp(reqURL *url.URL, n *xnode) string {
	if n == nil {
		return ""
	}
	h := n.child("href")
	if h == nil || strings.TrimSpace(h.Text) == "" {
		return ""
	}
	return c.resolveHref(reqURL, h.Text)
}

// ---------- discovery ----------

// Home finds the calendar home of the signed-in user (RFC 6764): the
// address itself, /.well-known/caldav and the server root are asked for
// the current-user-principal, then the principal for its calendar-home-set.
func (c *Client) Home(ctx context.Context) (string, error) {
	starts := []string{}
	if bp := c.BasePath(); bp != "" {
		starts = append(starts, bp)
	}
	starts = append(starts, "/.well-known/caldav", "/")
	var lastErr error
	for _, start := range starts {
		home, principal, err := c.askPrincipal(ctx, start)
		if errors.Is(err, ErrAuth) {
			return "", err
		}
		if err != nil {
			lastErr = err
		}
		if home == "" && principal != "" {
			home, err = c.homeSet(ctx, principal)
			if errors.Is(err, ErrAuth) {
				return "", err
			}
			if err != nil {
				lastErr = err
			}
		}
		if home != "" {
			if !strings.HasSuffix(home, "/") {
				home += "/"
			}
			return home, nil
		}
	}
	// Nextcloud's well-known layout when nothing answered
	if bp := c.BasePath(); strings.Contains(bp, "/remote.php/dav") && c.User != "" {
		root := bp[:strings.Index(bp, "/remote.php/dav")]
		return root + "/remote.php/dav/calendars/" + c.User + "/", nil
	}
	// the address may point straight at a calendar home or a calendar
	if bp := c.BasePath(); bp != "" {
		if cals, err := c.Calendars(ctx, bp); err == nil && len(cals) > 0 {
			if !strings.HasSuffix(bp, "/") {
				bp += "/"
			}
			return bp, nil
		}
	}
	if lastErr != nil {
		return "", fmt.Errorf("%w (%v)", ErrNoCalendars, lastErr)
	}
	return "", ErrNoCalendars
}

func (c *Client) askPrincipal(ctx context.Context, href string) (home, principal string, err error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:current-user-principal/><c:calendar-home-set/></d:prop></d:propfind>`
	status, _, b, reqURL, err := c.do(ctx, "PROPFIND", href, map[string]string{"Depth": "0", "Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return "", "", err
	}
	if status != 207 {
		return "", "", fmt.Errorf("PROPFIND %s: HTTP %d", href, status)
	}
	rs, _, err := c.parseMultistatus(reqURL, b)
	if err != nil {
		return "", "", err
	}
	for _, r := range rs {
		if h := c.hrefProp(reqURL, r.props["calendar-home-set"]); h != "" {
			home = h
		}
		if p := c.hrefProp(reqURL, r.props["current-user-principal"]); p != "" {
			principal = p
		}
	}
	return home, principal, nil
}

func (c *Client) homeSet(ctx context.Context, principal string) (string, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`
	status, _, b, reqURL, err := c.do(ctx, "PROPFIND", principal, map[string]string{"Depth": "0", "Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return "", err
	}
	if status != 207 {
		return "", fmt.Errorf("PROPFIND %s: HTTP %d", principal, status)
	}
	rs, _, err := c.parseMultistatus(reqURL, b)
	if err != nil {
		return "", err
	}
	for _, r := range rs {
		if h := c.hrefProp(reqURL, r.props["calendar-home-set"]); h != "" {
			return h, nil
		}
	}
	return "", nil
}

const calendarProps = `<d:prop><d:displayname/><d:resourcetype/><d:sync-token/><d:current-user-privilege-set/><c:supported-calendar-component-set/><x1:calendar-color/><x1:calendar-order/><cs:getctag/></d:prop>`

const calendarNS = `xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:x1="http://apple.com/ns/ical/" xmlns:cs="http://calendarserver.org/ns/"`

// Calendars lists the calendar collections of the home.
func (c *Client) Calendars(ctx context.Context, home string) ([]Calendar, error) {
	rs, err := c.propfindCalendars(ctx, home, "1")
	if err != nil {
		return nil, err
	}
	var out []Calendar
	for _, r := range rs {
		if cal, ok := readCalendar(r); ok {
			out = append(out, cal)
		}
	}
	return out, nil
}

// Collection reads one calendar's properties (name, colour, ctag, sync token).
func (c *Client) Collection(ctx context.Context, href string) (*Calendar, error) {
	rs, err := c.propfindCalendars(ctx, href, "0")
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if cal, ok := readCalendar(r); ok {
			cal.Href = href
			return &cal, nil
		}
	}
	return nil, ErrNotFound
}

func (c *Client) propfindCalendars(ctx context.Context, href, depth string) ([]response, error) {
	body := `<?xml version="1.0"?><d:propfind ` + calendarNS + `>` + calendarProps + `</d:propfind>`
	status, _, b, reqURL, err := c.do(ctx, "PROPFIND", href, map[string]string{"Depth": depth, "Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, ErrNotFound
	}
	if status != 207 {
		return nil, fmt.Errorf("listing calendars: HTTP %d", status)
	}
	rs, _, err := c.parseMultistatus(reqURL, b)
	return rs, err
}

func readCalendar(r response) (Calendar, bool) {
	rt := r.props["resourcetype"]
	if rt == nil || rt.child("calendar") == nil || rt.child("deleted-calendar") != nil {
		return Calendar{}, false
	}
	cal := Calendar{Href: r.href}
	if !strings.HasSuffix(cal.Href, "/") {
		cal.Href += "/"
	}
	if dn := r.props["displayname"]; dn != nil {
		cal.Name = strings.TrimSpace(dn.Text)
	}
	if col := r.props["calendar-color"]; col != nil {
		cal.Color = strings.TrimSpace(col.Text)
	}
	if ct := r.props["getctag"]; ct != nil {
		cal.CTag = strings.TrimSpace(ct.Text)
	}
	if st := r.props["sync-token"]; st != nil {
		cal.SyncToken = strings.TrimSpace(st.Text)
	}
	if o := r.props["calendar-order"]; o != nil {
		fmt.Sscanf(strings.TrimSpace(o.Text), "%d", &cal.Order)
	}
	if comps := r.props["supported-calendar-component-set"]; comps != nil {
		for _, cn := range comps.children("comp") {
			cal.Components = append(cal.Components, strings.ToUpper(cn.attr("name")))
		}
	}
	if priv := r.props["current-user-privilege-set"]; priv != nil {
		writable := false
		for _, p := range priv.children("privilege") {
			if p.child("write") != nil || p.child("write-content") != nil || p.child("all") != nil || p.child("bind") != nil {
				writable = true
			}
		}
		cal.ReadOnly = !writable
	}
	return cal, true
}

// HasComponent reports whether the calendar holds that kind.
func (cal Calendar) HasComponent(name string) bool {
	for _, c := range cal.Components {
		if c == name {
			return true
		}
	}
	return false
}

// HoldsEvents reports whether the calendar can hold VEVENTs (a server that
// does not say is taken to allow everything).
func (cal Calendar) HoldsEvents() bool {
	return len(cal.Components) == 0 || cal.HasComponent("VEVENT")
}

// ---------- listing and sync ----------

// ListETags lists every resource of a collection with its etag.
func (c *Client) ListETags(ctx context.Context, href string) ([]Change, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:resourcetype/></d:prop></d:propfind>`
	status, _, b, reqURL, err := c.do(ctx, "PROPFIND", href, map[string]string{"Depth": "1", "Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return nil, err
	}
	if status != 207 {
		return nil, fmt.Errorf("listing %s: HTTP %d", href, status)
	}
	rs, _, err := c.parseMultistatus(reqURL, b)
	if err != nil {
		return nil, err
	}
	var out []Change
	for _, r := range rs {
		if SameHref(r.href, href) {
			continue
		}
		if rt := r.props["resourcetype"]; rt != nil && rt.child("collection") != nil {
			continue
		}
		et := ""
		if e := r.props["getetag"]; e != nil {
			et = strings.TrimSpace(e.Text)
		}
		out = append(out, Change{Href: r.href, ETag: et})
	}
	return out, nil
}

// ListEvents lists the VEVENT resources of a calendar with their etags
// (calendar-query), falling back to a plain listing when the server will not.
func (c *Client) ListEvents(ctx context.Context, href string) ([]Change, error) {
	body := `<?xml version="1.0"?><c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop><c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"/></c:comp-filter></c:filter></c:calendar-query>`
	status, _, b, reqURL, err := c.do(ctx, "REPORT", href, map[string]string{"Depth": "1", "Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return nil, err
	}
	if status != 207 {
		return c.ListETags(ctx, href)
	}
	rs, _, err := c.parseMultistatus(reqURL, b)
	if err != nil {
		return c.ListETags(ctx, href)
	}
	var out []Change
	for _, r := range rs {
		if SameHref(r.href, href) || strings.HasSuffix(r.href, "/") {
			continue
		}
		et := ""
		if e := r.props["getetag"]; e != nil {
			et = strings.TrimSpace(e.Text)
		}
		out = append(out, Change{Href: r.href, ETag: et})
	}
	return out, nil
}

// Sync asks what changed since token (RFC 6578). An empty token lists everything.
func (c *Client) Sync(ctx context.Context, href, token string) ([]Change, string, error) {
	body := `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:"><d:sync-token>` + xmlEsc(token) + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	status, _, b, reqURL, err := c.do(ctx, "REPORT", href, map[string]string{"Depth": "0", "Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return nil, "", err
	}
	if status == 403 || status == 400 || status == 409 || status == 410 {
		return nil, "", ErrInvalidToken
	}
	if status != 207 {
		return nil, "", fmt.Errorf("sync of %s: HTTP %d", href, status)
	}
	rs, root, err := c.parseMultistatus(reqURL, b)
	if err != nil {
		return nil, "", err
	}
	var out []Change
	for _, r := range rs {
		if SameHref(r.href, href) || strings.HasSuffix(r.href, "/") {
			continue
		}
		if strings.Contains(r.status, " 404") {
			out = append(out, Change{Href: r.href, Deleted: true})
			continue
		}
		et := ""
		if e := r.props["getetag"]; e != nil {
			et = strings.TrimSpace(e.Text)
		}
		out = append(out, Change{Href: r.href, ETag: et})
	}
	newToken := ""
	if st := root.child("sync-token"); st != nil {
		newToken = strings.TrimSpace(st.Text)
	}
	return out, newToken, nil
}

// Multiget fetches resources by href, in batches of 50.
func (c *Client) Multiget(ctx context.Context, collection string, hrefs []string) ([]Resource, error) {
	var out []Resource
	for i := 0; i < len(hrefs); i += 50 {
		end := i + 50
		if end > len(hrefs) {
			end = len(hrefs)
		}
		var hb bytes.Buffer
		for _, h := range hrefs[i:end] {
			hb.WriteString("<d:href>" + xmlEsc(escapedPath(h)) + "</d:href>")
		}
		body := `<?xml version="1.0"?><c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop>` + hb.String() + `</c:calendar-multiget>`
		status, _, b, reqURL, err := c.do(ctx, "REPORT", collection, map[string]string{"Depth": "1", "Content-Type": "application/xml; charset=utf-8"}, body)
		if err != nil {
			return nil, err
		}
		if status != 207 {
			return nil, fmt.Errorf("fetching events: HTTP %d", status)
		}
		rs, _, err := c.parseMultistatus(reqURL, b)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			data := r.props["calendar-data"]
			if data == nil {
				continue
			}
			et := ""
			if e := r.props["getetag"]; e != nil {
				et = strings.TrimSpace(e.Text)
			}
			out = append(out, Resource{Href: r.href, ETag: et, Data: data.Text})
		}
	}
	return out, nil
}

// Get fetches one resource.
func (c *Client) Get(ctx context.Context, href string) (*Resource, error) {
	status, h, b, _, err := c.do(ctx, http.MethodGet, href, nil, "")
	if err != nil {
		return nil, err
	}
	if status == 404 || status == 410 {
		return nil, ErrNotFound
	}
	if status != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", href, status)
	}
	return &Resource{Href: href, ETag: h.Get("ETag"), Data: string(b)}, nil
}

// Put writes a resource; ifMatch guards an update, create=true a new one.
// It returns the new etag (fetched when the server did not say).
func (c *Client) Put(ctx context.Context, href, ics, ifMatch string, create bool) (string, error) {
	hdr := map[string]string{"Content-Type": "text/calendar; charset=utf-8"}
	if create {
		hdr["If-None-Match"] = "*"
	} else if ifMatch != "" {
		hdr["If-Match"] = ifMatch
	}
	status, h, b, _, err := c.do(ctx, http.MethodPut, href, hdr, ics)
	if err != nil {
		return "", err
	}
	switch {
	case status == 412:
		return "", ErrPrecondition
	case status == 404 || status == 410:
		return "", ErrNotFound
	case status < 200 || status >= 300:
		return "", fmt.Errorf("PUT %s: HTTP %d %s", href, status, strings.TrimSpace(truncate(string(b), 300)))
	}
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		return et, nil
	}
	// no (strong) etag in the answer: ask
	changes, err := c.ListETags(ctx, href[:strings.LastIndex(href, "/")+1])
	if err == nil {
		for _, ch := range changes {
			if ch.Href == href {
				return ch.ETag, nil
			}
		}
	}
	return "", nil
}

// Delete removes a resource (404 is fine).
func (c *Client) Delete(ctx context.Context, href, ifMatch string) error {
	hdr := map[string]string{}
	if ifMatch != "" {
		hdr["If-Match"] = ifMatch
	}
	status, _, _, _, err := c.do(ctx, http.MethodDelete, href, hdr, "")
	if err != nil {
		return err
	}
	if status == 412 {
		return ErrPrecondition
	}
	if status != 204 && status != 200 && status != 404 && status != 410 {
		return fmt.Errorf("DELETE %s: HTTP %d", href, status)
	}
	return nil
}

// MkCalendar creates an event collection.
func (c *Client) MkCalendar(ctx context.Context, href, name, color string) error {
	colorXML := ""
	if color != "" {
		colorXML = `<x1:calendar-color>` + xmlEsc(strings.ToUpper(color)) + `</x1:calendar-color>`
	}
	body := `<?xml version="1.0"?><c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:x1="http://apple.com/ns/ical/"><d:set><d:prop><d:displayname>` + xmlEsc(name) + `</d:displayname>` + colorXML + `<c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>`
	status, _, b, _, err := c.do(ctx, "MKCALENDAR", href, map[string]string{"Content-Type": "application/xml; charset=utf-8"}, body)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("MKCALENDAR %s: HTTP %d %s", href, status, strings.TrimSpace(truncate(string(b), 300)))
	}
	return nil
}

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
