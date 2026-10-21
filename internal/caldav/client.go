// Package caldav is a small CalDAV client: enough to read and write the
// task calendars of a Nextcloud (or any other CalDAV server).
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
	"strings"
	"time"
)

// Client talks to one server as one user.
type Client struct {
	Base string // https://cloud.example.com (no path)
	User string
	Pass string
	HTTP *http.Client
	UA   string
}

// ErrAuth is a 401/403.
var ErrAuth = errors.New("the server refused the sign-in")

// ErrInvalidToken says the sync token is no longer known; do a full listing.
var ErrInvalidToken = errors.New("sync token no longer valid")

// ErrPrecondition is a 412 on PUT/DELETE (someone else changed it).
var ErrPrecondition = errors.New("changed on the server meanwhile")

// ErrNotFound is a 404.
var ErrNotFound = errors.New("not found on the server")

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

func New(base, user, pass string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), User: user, Pass: pass, HTTP: &http.Client{Timeout: 60 * time.Second}, UA: "PocketTasks"}
}

func (c *Client) do(ctx context.Context, method, href string, headers map[string]string, body string) (int, http.Header, []byte, error) {
	u := href
	if !strings.HasPrefix(href, "http") {
		u = c.Base + href
	}
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.SetBasicAuth(c.User, c.Pass)
	req.Header.Set("User-Agent", c.UA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return res.StatusCode, res.Header, b, ErrAuth
	}
	return res.StatusCode, res.Header, b, nil
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

func parseMultistatus(b []byte) ([]response, *xnode, error) {
	var root xnode
	if err := xml.Unmarshal(b, &root); err != nil {
		return nil, nil, fmt.Errorf("unexpected answer: %w", err)
	}
	var out []response
	for _, r := range root.children("response") {
		resp := response{props: map[string]*xnode{}}
		if h := r.child("href"); h != nil {
			resp.href = strings.TrimSpace(h.Text)
			if u, err := url.PathUnescape(resp.href); err == nil {
				resp.href = u
			}
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

// Home finds the calendar home of the signed-in user.
func (c *Client) Home(ctx context.Context) (string, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`
	status, _, b, err := c.do(ctx, "PROPFIND", "/remote.php/dav/", map[string]string{"Depth": "0", "Content-Type": "application/xml"}, body)
	if err != nil {
		return "", err
	}
	principal := ""
	if status == 207 {
		if rs, _, err := parseMultistatus(b); err == nil {
			for _, r := range rs {
				if p := r.props["current-user-principal"]; p != nil {
					if h := p.child("href"); h != nil {
						principal = strings.TrimSpace(h.Text)
					}
				}
			}
		}
	}
	if principal != "" {
		body = `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`
		status, _, b, err = c.do(ctx, "PROPFIND", principal, map[string]string{"Depth": "0", "Content-Type": "application/xml"}, body)
		if err == nil && status == 207 {
			if rs, _, err := parseMultistatus(b); err == nil {
				for _, r := range rs {
					if p := r.props["calendar-home-set"]; p != nil {
						if h := p.child("href"); h != nil && strings.TrimSpace(h.Text) != "" {
							home := strings.TrimSpace(h.Text)
							if u, err := url.PathUnescape(home); err == nil {
								home = u
							}
							if !strings.HasSuffix(home, "/") {
								home += "/"
							}
							return home, nil
						}
					}
				}
			}
		}
	}
	return "/remote.php/dav/calendars/" + url.PathEscape(c.User) + "/", nil
}

// Calendars lists the collections of the home.
func (c *Client) Calendars(ctx context.Context, home string) ([]Calendar, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:x1="http://apple.com/ns/ical/" xmlns:cs="http://calendarserver.org/ns/"><d:prop><d:displayname/><d:resourcetype/><d:sync-token/><d:current-user-privilege-set/><c:supported-calendar-component-set/><x1:calendar-color/><x1:calendar-order/><cs:getctag/></d:prop></d:propfind>`
	status, _, b, err := c.do(ctx, "PROPFIND", home, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, body)
	if err != nil {
		return nil, err
	}
	if status != 207 {
		return nil, fmt.Errorf("listing calendars: HTTP %d", status)
	}
	rs, _, err := parseMultistatus(b)
	if err != nil {
		return nil, err
	}
	var out []Calendar
	for _, r := range rs {
		rt := r.props["resourcetype"]
		if rt == nil || rt.child("calendar") == nil || rt.child("deleted-calendar") != nil {
			continue
		}
		cal := Calendar{Href: r.href}
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
				if p.child("write") != nil || p.child("write-content") != nil || p.child("all") != nil {
					writable = true
				}
			}
			cal.ReadOnly = !writable
		}
		out = append(out, cal)
	}
	return out, nil
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

// ListETags lists every resource of a collection with its etag.
func (c *Client) ListETags(ctx context.Context, href string) ([]Change, error) {
	body := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:getetag/><d:resourcetype/></d:prop></d:propfind>`
	status, _, b, err := c.do(ctx, "PROPFIND", href, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, body)
	if err != nil {
		return nil, err
	}
	if status != 207 {
		return nil, fmt.Errorf("listing %s: HTTP %d", href, status)
	}
	rs, _, err := parseMultistatus(b)
	if err != nil {
		return nil, err
	}
	var out []Change
	for _, r := range rs {
		if r.href == href || strings.TrimSuffix(r.href, "/") == strings.TrimSuffix(href, "/") {
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

// Sync asks what changed since token (RFC 6578). An empty token lists everything.
func (c *Client) Sync(ctx context.Context, href, token string) ([]Change, string, error) {
	body := `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:"><d:sync-token>` + xmlEsc(token) + `</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
	status, _, b, err := c.do(ctx, "REPORT", href, map[string]string{"Depth": "0", "Content-Type": "application/xml"}, body)
	if err != nil {
		return nil, "", err
	}
	if status == 403 || status == 400 || status == 409 {
		return nil, "", ErrInvalidToken
	}
	if status != 207 {
		return nil, "", fmt.Errorf("sync of %s: HTTP %d", href, status)
	}
	rs, root, err := parseMultistatus(b)
	if err != nil {
		return nil, "", err
	}
	var out []Change
	for _, r := range rs {
		if strings.TrimSuffix(r.href, "/") == strings.TrimSuffix(href, "/") {
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

// Multiget fetches resources by href, in batches.
func (c *Client) Multiget(ctx context.Context, collection string, hrefs []string) ([]Resource, error) {
	var out []Resource
	for i := 0; i < len(hrefs); i += 50 {
		end := i + 50
		if end > len(hrefs) {
			end = len(hrefs)
		}
		var hb bytes.Buffer
		for _, h := range hrefs[i:end] {
			hb.WriteString("<d:href>" + xmlEsc(h) + "</d:href>")
		}
		body := `<?xml version="1.0"?><c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop>` + hb.String() + `</c:calendar-multiget>`
		status, _, b, err := c.do(ctx, "REPORT", collection, map[string]string{"Depth": "1", "Content-Type": "application/xml"}, body)
		if err != nil {
			return nil, err
		}
		if status != 207 {
			return nil, fmt.Errorf("fetching tasks: HTTP %d", status)
		}
		rs, _, err := parseMultistatus(b)
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
	status, h, b, err := c.do(ctx, http.MethodGet, href, nil, "")
	if err != nil {
		return nil, err
	}
	if status == 404 {
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
	status, h, b, err := c.do(ctx, http.MethodPut, href, hdr, ics)
	if err != nil {
		return "", err
	}
	switch {
	case status == 412:
		return "", ErrPrecondition
	case status == 404:
		return "", ErrNotFound
	case status < 200 || status >= 300:
		return "", fmt.Errorf("PUT %s: HTTP %d %s", href, status, strings.TrimSpace(string(b)))
	}
	if et := h.Get("ETag"); et != "" {
		return et, nil
	}
	// no etag in the answer: ask
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
	status, _, _, err := c.do(ctx, http.MethodDelete, href, hdr, "")
	if err != nil {
		return err
	}
	if status == 412 {
		return ErrPrecondition
	}
	if status != 204 && status != 200 && status != 404 {
		return fmt.Errorf("DELETE %s: HTTP %d", href, status)
	}
	return nil
}

// MkCalendar creates a VTODO collection.
func (c *Client) MkCalendar(ctx context.Context, href, name, color string) error {
	colorXML := ""
	if color != "" {
		colorXML = `<x1:calendar-color>` + xmlEsc(strings.ToUpper(color)) + `</x1:calendar-color>`
	}
	body := `<?xml version="1.0"?><c:mkcalendar xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:x1="http://apple.com/ns/ical/"><d:set><d:prop><d:displayname>` + xmlEsc(name) + `</d:displayname>` + colorXML + `<c:supported-calendar-component-set><c:comp name="VTODO"/></c:supported-calendar-component-set></d:prop></d:set></c:mkcalendar>`
	status, _, b, err := c.do(ctx, "MKCALENDAR", href, map[string]string{"Content-Type": "application/xml"}, body)
	if err != nil {
		return err
	}
	if status != 201 {
		return fmt.Errorf("MKCALENDAR %s: HTTP %d %s", href, status, strings.TrimSpace(string(b)))
	}
	return nil
}

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
