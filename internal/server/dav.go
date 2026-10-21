package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-calendar/internal/ical"
	"github.com/abjelosevic88/pocket-homelab-calendar/internal/store"
)

// A CalDAV server (RFC 4791, RFC 6578 and the Apple and Nextcloud
// extensions clients ask for). Paths, under /dav/ or the Nextcloud-shaped
// /remote.php/dav/:
//
//	/                                  root: current-user-principal
//	/principals/<user>/                the person (also /principals/users/<user>/)
//	/calendars/<user>/                 calendar home
//	/calendars/<user>/<calendar uid>/  one of the person's own calendars
//	/calendars/<user>/shared-<id>/     a calendar someone shared with them
//	/calendars/<user>/invitations/     read-only: events they were invited to
//	/calendars/<user>/<cal>/<name>     one event (master VEVENT + overrides)
//
// <user> is the person's CalDAV name (or email, or "me"); another person's
// name answers 404, so nobody learns who else is on the box.
//
// Shared calendars carry the role of the share: read and freebusy are
// read-only; with read, private events show as "Busy" (as on the web), and
// with freebusy every event does.

const (
	nsDAV    = "DAV:"
	nsCalDAV = "urn:ietf:params:xml:ns:caldav"
	nsCS     = "http://calendarserver.org/ns/"
	nsApple  = "http://apple.com/ns/ical/"
	nsOC     = "http://owncloud.org/ns"

	syncPfx        = "http://pocket-calendar/sync/"
	invitationsSeg = "invitations"
	sharedPfx      = "shared-"
	davCaps        = "1, 3, calendar-access, extended-mkcol"
	maxICSBytes    = 10 << 20
)

type davReq struct {
	prefix string // "/dav" or "/remote.php/dav"
	user   *store.User
	parts  []string // path segments after the prefix
	depth  string
	cols   []*davColl // what the home holds, loaded once per request
	loaded bool
}

// davColl is one calendar collection of the home.
type davColl struct {
	seg  string
	kind string          // calendar | invitations
	cal  *store.Calendar // nil for invitations (filled per viewer: role, prefs, owner)
	role string
}

// writable reports whether events may be written into the collection.
func (c *davColl) writable() bool {
	return c.kind == "calendar" && store.RoleAtLeast(c.role, store.RoleWrite) && !c.cal.ReadOnly
}

func (c *davColl) own() bool { return c.kind == "calendar" && c.role == store.RoleOwner }

// handleDAV routes every request under the DAV prefixes.
func (s *Server) handleDAV(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	prefix := "/dav"
	switch {
	case strings.HasPrefix(p, "/remote.php/dav"):
		prefix = "/remote.php/dav"
	case strings.HasPrefix(p, "/principals/"):
		prefix = "" // /principals/<user>/ as Apple sometimes derives it
	}
	rest := strings.TrimPrefix(p, prefix)
	var parts []string
	for _, seg := range strings.Split(rest, "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}
	if r.Method == http.MethodOptions {
		s.davOptions(w)
		return
	}
	u, err := s.resolveUser(r)
	if err != nil || u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Pocket Calendar"`)
		w.Header().Set("DAV", davCaps)
		http.Error(w, "sign in with your CalDAV name and an API token (Settings → Apps & API)", http.StatusUnauthorized)
		return
	}
	if prefix == "" {
		prefix = "/dav" // the path already starts with principals/
	}
	// Nextcloud keeps people under principals/users/<user>/
	if len(parts) >= 2 && parts[0] == "principals" && parts[1] == "users" {
		parts = append([]string{"principals"}, parts[2:]...)
	}
	req := &davReq{prefix: prefix, user: u, parts: parts, depth: strings.ToLower(r.Header.Get("Depth"))}
	if req.depth == "" {
		req.depth = "infinity"
	}
	ctx := r.Context()
	w.Header().Set("DAV", davCaps)
	switch {
	case len(parts) == 0:
		s.davRoot(ctx, w, r, req)
	case len(parts) == 1 && (parts[0] == "principals" || parts[0] == "calendars"):
		s.davRoot(ctx, w, r, req)
	case len(parts) == 1:
		s.davNotFound(w)
	case !s.davUserMatches(parts[1], u):
		s.davNotFound(w)
	case parts[0] == "principals" && len(parts) == 2:
		s.davPrincipal(ctx, w, r, req)
	case parts[0] == "calendars" && len(parts) == 2:
		s.davHome(ctx, w, r, req)
	case parts[0] == "calendars" && len(parts) == 3:
		s.davCollection(ctx, w, r, req, parts[2])
	case parts[0] == "calendars" && len(parts) == 4:
		s.davResource(ctx, w, r, req, parts[2], parts[3])
	default:
		s.davNotFound(w)
	}
}

func (s *Server) davUserMatches(seg string, u *store.User) bool {
	seg = strings.ToLower(seg)
	email := strings.ToLower(u.Email)
	return seg == "me" || (u.Username != "" && seg == strings.ToLower(u.Username)) || seg == email ||
		seg == strconv.FormatInt(u.ID, 10) || seg == strings.Split(email, "@")[0]
}

func (s *Server) davOptions(w http.ResponseWriter) {
	w.Header().Set("DAV", davCaps)
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT, MKCALENDAR, MKCOL")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) davNotFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

func (s *Server) davMethodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// davError answers with a DAV:error body naming the failed precondition.
func davError(w http.ResponseWriter, status int, cond string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<d:error xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">` + cond + `</d:error>` + "\n"))
}

func (s *Server) davFail(w http.ResponseWriter, err error, what string) {
	s.log.Error("caldav: "+what, "err", err)
	http.Error(w, what+": "+err.Error(), http.StatusInternalServerError)
}

// ---------- XML in ----------

type xnode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Text    string     `xml:",chardata"`
	Nodes   []xnode    `xml:",any"`
}

func (n *xnode) child(local string) *xnode {
	if n == nil {
		return nil
	}
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			return &n.Nodes[i]
		}
	}
	return nil
}

func (n *xnode) children(local string) []*xnode {
	if n == nil {
		return nil
	}
	var out []*xnode
	for i := range n.Nodes {
		if n.Nodes[i].XMLName.Local == local {
			out = append(out, &n.Nodes[i])
		}
	}
	return out
}

// propNodes are the properties under a set/remove element.
func (n *xnode) propNodes() []xnode {
	if p := n.child("prop"); p != nil {
		return p.Nodes
	}
	return nil
}

func (n *xnode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) textOr(def string) string {
	if n == nil {
		return def
	}
	return n.Text
}

func readXML(r *http.Request) (*xnode, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}
	var n xnode
	if err := xml.Unmarshal(body, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// propName is a requested property.
type propName struct{ Space, Local string }

func requestedProps(n *xnode) (props []propName, allprop bool) {
	if n == nil {
		return nil, true
	}
	if n.child("allprop") != nil || n.child("propname") != nil {
		return nil, true
	}
	p := n.child("prop")
	if p == nil {
		return nil, true
	}
	for _, c := range p.Nodes {
		props = append(props, propName{c.XMLName.Space, c.XMLName.Local})
	}
	return props, false
}

func propTag(p xnode) string {
	return `<x:` + p.XMLName.Local + ` xmlns:x="` + xesc(p.XMLName.Space) + `"/>`
}

// ---------- XML out ----------

type propstat struct {
	found    []string // rendered <prop> children
	notFound []propName
}

func (ps *propstat) add(xmlFragment string) { ps.found = append(ps.found, xmlFragment) }

func xesc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func hrefEsc(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

func hrefXML(p string) string { return "<d:href>" + xesc(hrefEsc(p)) + "</d:href>" }

type msWriter struct {
	b strings.Builder
}

func newMultistatus() *msWriter {
	m := &msWriter{}
	m.b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	m.b.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav" xmlns:cs="http://calendarserver.org/ns/" xmlns:ical="http://apple.com/ns/ical/" xmlns:oc="http://owncloud.org/ns">` + "\n")
	return m
}

// response writes one resource; href is a path (escaped here).
func (m *msWriter) response(href string, ps *propstat) {
	m.responseRaw(hrefEsc(href), ps)
}

// responseRaw writes one resource under an href already in wire form
// (multiget echoes what the client asked for).
func (m *msWriter) responseRaw(href string, ps *propstat) {
	m.b.WriteString("<d:response><d:href>" + xesc(href) + "</d:href>")
	if len(ps.found) > 0 {
		m.b.WriteString("<d:propstat><d:prop>" + strings.Join(ps.found, "") + "</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>")
	}
	if len(ps.notFound) > 0 {
		m.b.WriteString("<d:propstat><d:prop>")
		for _, p := range ps.notFound {
			m.b.WriteString(`<x:` + p.Local + ` xmlns:x="` + xesc(p.Space) + `"/>`)
		}
		m.b.WriteString("</d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>")
	}
	if len(ps.found) == 0 && len(ps.notFound) == 0 {
		m.b.WriteString("<d:propstat><d:prop/><d:status>HTTP/1.1 200 OK</d:status></d:propstat>")
	}
	m.b.WriteString("</d:response>\n")
}

func (m *msWriter) gone(href string) { m.goneRaw(hrefEsc(href)) }

func (m *msWriter) goneRaw(href string) {
	m.b.WriteString("<d:response><d:href>" + xesc(href) + "</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>\n")
}

func (m *msWriter) write(w http.ResponseWriter, extra string) {
	m.b.WriteString(extra)
	m.b.WriteString("</d:multistatus>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(m.b.String()))
}

// ---------- the home's collections ----------

func principalName(u *store.User) string { return firstNonEmpty(u.Username, u.Email) }

func (s *Server) principalHref(req *davReq) string {
	if req.prefix == "/remote.php/dav" {
		return req.prefix + "/principals/users/" + principalName(req.user) + "/"
	}
	return req.prefix + "/principals/" + principalName(req.user) + "/"
}

func (s *Server) homeHref(req *davReq) string {
	return req.prefix + "/calendars/" + principalName(req.user) + "/"
}

func sharedSeg(calID int64) string { return sharedPfx + strconv.FormatInt(calID, 10) }

// reservedSeg reports whether a path segment names a collection the server
// makes up (so MKCALENDAR may not take it).
func reservedSeg(seg string) bool {
	if strings.EqualFold(seg, invitationsSeg) {
		return true
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(seg), sharedPfx); ok {
		_, err := strconv.ParseInt(rest, 10, 64)
		return err == nil
	}
	return false
}

// collections lists what the person's calendar home holds: their own
// calendars, the ones shared with them (minus those they left) and the
// Invitations collection.
func (s *Server) collections(ctx context.Context, req *davReq) ([]*davColl, error) {
	if req.loaded {
		return req.cols, nil
	}
	uid := req.user.ID
	cals, err := s.store.ListCalendarsFor(ctx, uid)
	if err != nil {
		return nil, err
	}
	if len(cals) == 0 {
		s.seedUser(ctx, req.user)
		if cals, err = s.store.ListCalendarsFor(ctx, uid); err != nil {
			return nil, err
		}
	}
	own := map[string]bool{}
	for _, c := range cals {
		if c.Role == store.RoleOwner {
			own[c.UID] = true
		}
	}
	var out []*davColl
	for _, c := range cals {
		switch {
		case c.Role == store.RoleOwner:
			out = append(out, &davColl{seg: c.UID, kind: "calendar", cal: c, role: c.Role})
		case c.Role == "":
		case !c.Visible && s.store.UserSetting(ctx, uid, "left."+itoa64(c.ID)) == "1":
			// left: gone from the person's lists, CalDAV included
		case own[sharedSeg(c.ID)]:
			// an own calendar took the name; the share stays reachable on the web
		default:
			out = append(out, &davColl{seg: sharedSeg(c.ID), kind: "calendar", cal: c, role: c.Role})
		}
	}
	if !own[invitationsSeg] {
		out = append(out, &davColl{seg: invitationsSeg, kind: "invitations", role: store.RoleRead})
	}
	req.cols, req.loaded = out, true
	return out, nil
}

func (s *Server) resolveCollection(ctx context.Context, req *davReq, seg string) (*davColl, error) {
	cols, err := s.collections(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		if c.seg == seg {
			return c, nil
		}
	}
	return nil, store.ErrNotFound
}

// ---------- masking ----------

const (
	maskNone = iota
	maskPrivate
	maskAll
)

func maskModeOf(c *davColl) int {
	if c.kind != "calendar" {
		return maskNone
	}
	switch {
	case c.role == store.RoleFreeBusy:
		return maskAll
	case !store.RoleAtLeast(c.role, store.RoleWrite):
		return maskPrivate
	}
	return maskNone
}

// busyKeep are the VEVENT properties a masked event keeps: when, how it
// repeats and whether it blocks time.
var busyKeep = []string{"UID", "DTSTART", "DTEND", "DURATION", "RRULE", "EXDATE", "RDATE", "RECURRENCE-ID", "DTSTAMP", "SEQUENCE", "TRANSP", "STATUS"}

// maskObject turns events the viewer may not read into "Busy" blocks:
// every VEVENT for freebusy, CLASS:PRIVATE / CONFIDENTIAL ones for read.
func maskObject(ics string, mode int) (string, bool) {
	if mode == maskNone {
		return ics, false
	}
	cal, err := ical.Parse(ics)
	if err != nil {
		return ical.NewCalendar().String(), true
	}
	changed := false
	for i, ch := range cal.Children {
		if ch.Name != "VEVENT" {
			continue
		}
		class := strings.ToUpper(ch.Value("CLASS"))
		if mode == maskPrivate && (class == "" || class == "PUBLIC") {
			continue
		}
		busy := &ical.Component{Name: "VEVENT"}
		for _, n := range busyKeep {
			busy.Props = append(busy.Props, ch.GetAll(n)...)
		}
		busy.SetText("SUMMARY", "Busy")
		busy.SetRaw("CLASS", "PRIVATE")
		cal.Children[i] = busy
		changed = true
	}
	if !changed {
		return ics, false
	}
	return cal.String(), true
}

// ---------- nodes and properties ----------

type davNode struct {
	kind     string // root | principal | home | calendar | invitations | resource
	href     string
	coll     *davColl
	row      *store.EventRow
	etag     string
	ics      string
	modified string
	ctag     string // invitations: digest of what it holds
	req      *davReq
}

func (s *Server) collNode(ctx context.Context, req *davReq, c *davColl) *davNode {
	n := &davNode{kind: c.kind, href: s.homeHref(req) + c.seg + "/", coll: c, req: req}
	if c.kind == "calendar" {
		n.modified = c.cal.UpdatedAt
	} else {
		n.ctag = s.invitationsTag(ctx, req)
	}
	return n
}

func resourceName(c *davColl, row *store.EventRow) string {
	if c.kind == "invitations" {
		return strconv.FormatInt(row.ID, 10) + ".ics"
	}
	return row.Href
}

func (s *Server) eventNode(req *davReq, c *davColl, row *store.EventRow) *davNode {
	ics, etag := row.ICS, row.ETag
	if masked, changed := maskObject(row.ICS, maskModeOf(c)); changed {
		ics, etag = masked, ical.ETag(masked)
	}
	return &davNode{kind: "resource", href: s.homeHref(req) + c.seg + "/" + resourceName(c, row), coll: c, row: row,
		etag: etag, ics: ics, modified: row.UpdatedAt, req: req}
}

func (s *Server) collDisplayName(c *davColl) string {
	if c.kind == "invitations" {
		return "Invitations"
	}
	if c.role != store.RoleOwner && c.cal.Owner != nil {
		return c.cal.Name + " (" + firstNonEmpty(c.cal.Owner.Name, c.cal.Owner.Email) + ")"
	}
	return c.cal.Name
}

func (s *Server) collColor(ctx context.Context, req *davReq, c *davColl) string {
	col := ""
	if c.kind == "invitations" {
		st, _ := s.store.GetSettings(ctx, req.user.ID)
		col = st.InvitesColor
	} else {
		col = firstNonEmpty(c.cal.MyColor, c.cal.Color)
	}
	if col == "" || !validColor(col) {
		col = palette[0]
	}
	return strings.ToUpper(col) + "FF"
}

const (
	privRead  = "<d:privilege><d:read/></d:privilege><d:privilege><d:read-current-user-privilege-set/></d:privilege>"
	privWrite = "<d:privilege><d:write/></d:privilege><d:privilege><d:write-properties/></d:privilege><d:privilege><d:write-content/></d:privilege><d:privilege><d:bind/></d:privilege><d:privilege><d:unbind/></d:privilege>"
)

// props answers a property request for one node.
func (s *Server) props(ctx context.Context, n *davNode, want []propName, all bool) *propstat {
	ps := &propstat{}
	req := n.req
	u := req.user
	isColl := n.kind == "calendar" || n.kind == "invitations"
	get := func(space, local string) (string, bool) {
		switch space + " " + local {
		case nsDAV + " resourcetype":
			switch n.kind {
			case "principal":
				return "<d:resourcetype><d:principal/><d:collection/></d:resourcetype>", true
			case "calendar", "invitations":
				return "<d:resourcetype><d:collection/><cal:calendar/></d:resourcetype>", true
			case "resource":
				return "<d:resourcetype/>", true
			}
			return "<d:resourcetype><d:collection/></d:resourcetype>", true
		case nsDAV + " displayname":
			switch n.kind {
			case "principal", "home":
				return "<d:displayname>" + xesc(firstNonEmpty(u.Name, u.Email)) + "</d:displayname>", true
			case "calendar", "invitations":
				return "<d:displayname>" + xesc(s.collDisplayName(n.coll)) + "</d:displayname>", true
			case "resource":
				return "", false
			}
			return "<d:displayname>Pocket Calendar</d:displayname>", true
		case nsDAV + " current-user-principal":
			return "<d:current-user-principal>" + hrefXML(s.principalHref(req)) + "</d:current-user-principal>", true
		case nsDAV + " principal-URL":
			if n.kind == "principal" {
				return "<d:principal-URL>" + hrefXML(s.principalHref(req)) + "</d:principal-URL>", true
			}
		case nsDAV + " principal-collection-set":
			return "<d:principal-collection-set>" + hrefXML(req.prefix+"/principals/") + "</d:principal-collection-set>", true
		case nsDAV + " owner":
			if n.kind == "principal" || n.kind == "home" || (n.coll != nil && (n.coll.kind == "invitations" || n.coll.own())) {
				return "<d:owner>" + hrefXML(s.principalHref(req)) + "</d:owner>", true
			}
		case nsOC + " owner-principal":
			if n.kind == "calendar" && n.coll.own() {
				return "<oc:owner-principal>" + xesc(hrefEsc(strings.TrimSuffix(s.principalHref(req), "/"))) + "</oc:owner-principal>", true
			}
		case nsCalDAV + " calendar-home-set":
			if n.kind == "principal" {
				return "<cal:calendar-home-set>" + hrefXML(s.homeHref(req)) + "</cal:calendar-home-set>", true
			}
		case nsCalDAV + " calendar-user-address-set":
			if n.kind == "principal" {
				return "<cal:calendar-user-address-set><d:href>mailto:" + xesc(u.Email) + "</d:href>" + hrefXML(s.principalHref(req)) + "</cal:calendar-user-address-set>", true
			}
		case nsCalDAV + " calendar-user-type":
			if n.kind == "principal" {
				return "<cal:calendar-user-type>INDIVIDUAL</cal:calendar-user-type>", true
			}
		case nsCS + " email-address-set":
			if n.kind == "principal" {
				return "<cs:email-address-set><cs:email-address>" + xesc(u.Email) + "</cs:email-address></cs:email-address-set>", true
			}
		case nsDAV + " current-user-privilege-set":
			priv := privRead
			if n.kind == "home" || (n.coll != nil && n.coll.writable()) {
				priv += privWrite
			}
			return "<d:current-user-privilege-set>" + priv + "</d:current-user-privilege-set>", true
		case nsDAV + " supported-report-set":
			if isColl || n.kind == "home" {
				return "<d:supported-report-set><d:supported-report><d:report><cal:calendar-query/></d:report></d:supported-report><d:supported-report><d:report><cal:calendar-multiget/></d:report></d:supported-report><d:supported-report><d:report><d:sync-collection/></d:report></d:supported-report></d:supported-report-set>", true
			}
			return "<d:supported-report-set/>", true
		case nsCalDAV + " supported-calendar-component-set":
			if isColl {
				return `<cal:supported-calendar-component-set><cal:comp name="VEVENT"/></cal:supported-calendar-component-set>`, true
			}
		case nsCalDAV + " supported-calendar-data":
			if isColl {
				return `<cal:supported-calendar-data><cal:calendar-data content-type="text/calendar" version="2.0"/></cal:supported-calendar-data>`, true
			}
		case nsCalDAV + " calendar-description":
			if n.kind == "calendar" {
				return "<cal:calendar-description>" + xesc(n.coll.cal.Description) + "</cal:calendar-description>", true
			}
			if n.kind == "invitations" {
				return "<cal:calendar-description>Events people on the box invited you to. Answer them in Pocket Calendar.</cal:calendar-description>", true
			}
		case nsCalDAV + " calendar-timezone":
			if n.kind == "calendar" && n.coll.cal.Timezone != "" {
				if tz := vtimezoneICS(n.coll.cal.Timezone); tz != "" {
					return "<cal:calendar-timezone>" + xesc(tz) + "</cal:calendar-timezone>", true
				}
			}
		case nsCalDAV + " calendar-timezone-id":
			if n.kind == "calendar" && n.coll.cal.Timezone != "" {
				return "<cal:calendar-timezone-id>" + xesc(n.coll.cal.Timezone) + "</cal:calendar-timezone-id>", true
			}
		case nsCalDAV + " max-resource-size":
			if isColl {
				return "<cal:max-resource-size>" + strconv.Itoa(maxICSBytes) + "</cal:max-resource-size>", true
			}
		case nsApple + " calendar-color":
			if isColl {
				return "<ical:calendar-color>" + xesc(s.collColor(ctx, req, n.coll)) + "</ical:calendar-color>", true
			}
		case nsApple + " calendar-order":
			if n.kind == "calendar" {
				return "<ical:calendar-order>" + strconv.Itoa(int(n.coll.cal.SortOrder)) + "</ical:calendar-order>", true
			}
			if n.kind == "invitations" {
				return "<ical:calendar-order>999</ical:calendar-order>", true
			}
		case nsOC + " calendar-enabled":
			if isColl {
				return "<oc:calendar-enabled>1</oc:calendar-enabled>", true
			}
		case nsCS + " getctag":
			if n.kind == "calendar" {
				return "<cs:getctag>" + strconv.FormatInt(n.coll.cal.Seq, 10) + "</cs:getctag>", true
			}
			if n.kind == "invitations" {
				return "<cs:getctag>" + xesc(n.ctag) + "</cs:getctag>", true
			}
		case nsDAV + " sync-token":
			if n.kind == "calendar" {
				return "<d:sync-token>" + xesc(syncPfx+strconv.FormatInt(n.coll.cal.Seq, 10)) + "</d:sync-token>", true
			}
			if n.kind == "invitations" {
				return "<d:sync-token>" + xesc(syncPfx+"inv-"+n.ctag) + "</d:sync-token>", true
			}
		case nsCalDAV + " schedule-calendar-transp":
			if isColl {
				return "<cal:schedule-calendar-transp><cal:opaque/></cal:schedule-calendar-transp>", true
			}
		case nsDAV + " getetag":
			if n.kind == "resource" {
				return "<d:getetag>" + xesc(n.etag) + "</d:getetag>", true
			}
		case nsDAV + " getcontenttype":
			if n.kind == "resource" {
				return "<d:getcontenttype>text/calendar; charset=utf-8; component=VEVENT</d:getcontenttype>", true
			}
		case nsDAV + " getcontentlength":
			if n.kind == "resource" {
				return "<d:getcontentlength>" + strconv.Itoa(len(n.ics)) + "</d:getcontentlength>", true
			}
		case nsDAV + " getlastmodified":
			if t := store.ParseTime(n.modified); !t.IsZero() {
				return "<d:getlastmodified>" + t.UTC().Format(http.TimeFormat) + "</d:getlastmodified>", true
			}
		case nsCalDAV + " calendar-data":
			if n.kind == "resource" {
				return "<cal:calendar-data>" + xesc(n.ics) + "</cal:calendar-data>", true
			}
		}
		return "", false
	}
	if all {
		for _, p := range defaultProps(n.kind) {
			if v, ok := get(p.Space, p.Local); ok {
				ps.add(v)
			}
		}
		return ps
	}
	for _, p := range want {
		if v, ok := get(p.Space, p.Local); ok {
			ps.add(v)
		} else {
			ps.notFound = append(ps.notFound, p)
		}
	}
	return ps
}

func defaultProps(kind string) []propName {
	common := []propName{{nsDAV, "resourcetype"}, {nsDAV, "displayname"}, {nsDAV, "current-user-principal"}, {nsDAV, "owner"}, {nsDAV, "supported-report-set"}, {nsDAV, "current-user-privilege-set"}}
	switch kind {
	case "principal":
		return append(common, propName{nsCalDAV, "calendar-home-set"}, propName{nsDAV, "principal-URL"}, propName{nsCalDAV, "calendar-user-address-set"})
	case "calendar", "invitations":
		return append(common, propName{nsCalDAV, "supported-calendar-component-set"}, propName{nsCalDAV, "calendar-description"},
			propName{nsApple, "calendar-color"}, propName{nsApple, "calendar-order"}, propName{nsCS, "getctag"}, propName{nsDAV, "sync-token"},
			propName{nsCalDAV, "calendar-timezone-id"})
	case "resource":
		return []propName{{nsDAV, "resourcetype"}, {nsDAV, "getetag"}, {nsDAV, "getcontenttype"}, {nsDAV, "getcontentlength"}, {nsDAV, "getlastmodified"}}
	}
	return common
}

// ---------- root, principal, home ----------

func (s *Server) davRoot(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq) {
	if r.Method != "PROPFIND" {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprintf(w, "Pocket Calendar CalDAV. Calendar home: %s\n", s.homeHref(req))
			return
		}
		s.davMethodNotAllowed(w)
		return
	}
	body, err := readXML(r)
	if err != nil {
		http.Error(w, "bad xml", http.StatusBadRequest)
		return
	}
	want, all := requestedProps(body)
	ms := newMultistatus()
	self := req.prefix + "/" + strings.Join(req.parts, "/")
	if len(req.parts) > 0 {
		self += "/"
	}
	ms.response(self, s.props(ctx, &davNode{kind: "root", req: req}, want, all))
	if req.depth != "0" {
		switch {
		case len(req.parts) == 0:
			ms.response(req.prefix+"/principals/", s.props(ctx, &davNode{kind: "root", req: req}, want, all))
			ms.response(req.prefix+"/calendars/", s.props(ctx, &davNode{kind: "root", req: req}, want, all))
		case req.parts[0] == "principals":
			ms.response(s.principalHref(req), s.props(ctx, &davNode{kind: "principal", req: req}, want, all))
		case req.parts[0] == "calendars":
			ms.response(s.homeHref(req), s.props(ctx, &davNode{kind: "home", req: req}, want, all))
		}
	}
	ms.write(w, "")
}

func (s *Server) davPrincipal(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq) {
	switch r.Method {
	case "PROPFIND", "REPORT":
		// a REPORT here is principal-property-search or expand-property:
		// answer with the one principal there is
		body, err := readXML(r)
		if err != nil {
			http.Error(w, "bad xml", http.StatusBadRequest)
			return
		}
		want, all := requestedProps(body)
		ms := newMultistatus()
		ms.response(s.principalHref(req), s.props(ctx, &davNode{kind: "principal", req: req}, want, all))
		ms.write(w, "")
	case http.MethodGet, http.MethodHead:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Principal %s\n", principalName(req.user))
	default:
		s.davMethodNotAllowed(w)
	}
}

func (s *Server) davHome(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq) {
	switch r.Method {
	case "PROPFIND":
		body, err := readXML(r)
		if err != nil {
			http.Error(w, "bad xml", http.StatusBadRequest)
			return
		}
		want, all := requestedProps(body)
		cols, err := s.collections(ctx, req)
		if err != nil {
			s.davFail(w, err, "list calendars")
			return
		}
		ms := newMultistatus()
		ms.response(s.homeHref(req), s.props(ctx, &davNode{kind: "home", req: req}, want, all))
		if req.depth != "0" {
			for _, c := range cols {
				n := s.collNode(ctx, req, c)
				ms.response(n.href, s.props(ctx, n, want, all))
			}
		}
		ms.write(w, "")
	case "PROPPATCH":
		// clients store defaults (alarms, …) on the home; take them without keeping them
		body, err := readXML(r)
		if err != nil || body == nil {
			http.Error(w, "bad xml", http.StatusBadRequest)
			return
		}
		var ok []string
		for _, part := range append(body.children("set"), body.children("remove")...) {
			for _, p := range part.propNodes() {
				ok = append(ok, propTag(p))
			}
		}
		writeProppatch(w, s.homeHref(req), ok, nil)
	case "REPORT":
		newMultistatus().write(w, "")
	case http.MethodGet, http.MethodHead:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Calendar home of %s\n", req.user.Email)
	default:
		s.davMethodNotAllowed(w)
	}
}

// ---------- collections ----------

func (s *Server) davCollection(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, seg string) {
	c, err := s.resolveCollection(ctx, req, seg)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.davFail(w, err, "list calendars")
			return
		}
		switch r.Method {
		case "MKCALENDAR", "MKCOL":
			s.davMkCalendar(ctx, w, r, req, seg)
		default:
			s.davNotFound(w)
		}
		return
	}
	n := s.collNode(ctx, req, c)
	switch r.Method {
	case "MKCALENDAR", "MKCOL":
		davError(w, http.StatusMethodNotAllowed, "<d:resource-must-be-null/>")
	case "PROPFIND":
		body, err := readXML(r)
		if err != nil {
			http.Error(w, "bad xml", http.StatusBadRequest)
			return
		}
		want, all := requestedProps(body)
		ms := newMultistatus()
		ms.response(n.href, s.props(ctx, n, want, all))
		if req.depth != "0" {
			rows, err := s.collectionRows(ctx, req, c)
			if err != nil {
				s.davFail(w, err, "list events")
				return
			}
			for _, row := range rows {
				en := s.eventNode(req, c, row)
				ms.response(en.href, s.props(ctx, en, want, all))
			}
		}
		ms.write(w, "")
	case "PROPPATCH":
		s.davProppatch(ctx, w, r, req, c, n.href)
	case "REPORT":
		s.davReport(ctx, w, r, req, c, n)
	case http.MethodDelete:
		s.davDeleteCollection(ctx, w, req, c)
	case http.MethodGet, http.MethodHead:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "Calendar %q\n", s.collDisplayName(c))
	default:
		s.davMethodNotAllowed(w)
	}
}

// collectionRows lists the events of a collection.
func (s *Server) collectionRows(ctx context.Context, req *davReq, c *davColl) ([]*store.EventRow, error) {
	if c.kind == "invitations" {
		return s.invitedRows(ctx, req)
	}
	return s.store.ListEvents(ctx, c.cal.ID)
}

// invitedRows are the events a person is an attendee of that do not show in
// one of their calendars already (declined ones left out).
func (s *Server) invitedRows(ctx context.Context, req *davReq) ([]*store.EventRow, error) {
	cols, err := s.collections(ctx, req)
	if err != nil {
		return nil, err
	}
	readable := map[int64]bool{}
	for _, c := range cols {
		if c.kind == "calendar" && store.RoleAtLeast(c.role, store.RoleRead) {
			readable[c.cal.ID] = true
		}
	}
	rows, err := s.store.InvitedEvents(ctx, req.user.ID, time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), ical.Forever)
	if err != nil {
		return nil, err
	}
	var out []*store.EventRow
	seen := map[int64]bool{}
	for _, row := range rows {
		if seen[row.ID] || readable[row.CalendarID] {
			continue
		}
		seen[row.ID] = true
		if a := s.store.MyAttendance(ctx, row.ID, req.user.ID); a != nil && a.PartStat == "DECLINED" {
			continue
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// invitationsTag is the ctag of the Invitations collection: a digest of
// which events it holds and their versions.
func (s *Server) invitationsTag(ctx context.Context, req *davReq) string {
	rows, _ := s.invitedRows(ctx, req)
	h := sha256.New()
	for _, row := range rows {
		fmt.Fprintf(h, "%d:%s;", row.ID, row.ETag)
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func (s *Server) davMkCalendar(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, seg string) {
	if reservedSeg(seg) {
		http.Error(w, "that name is taken by the server", http.StatusForbidden)
		return
	}
	body, err := readXML(r)
	if err != nil {
		http.Error(w, "bad xml", http.StatusBadRequest)
		return
	}
	own, _ := s.store.OwnCalendars(ctx, req.user.ID)
	c := &store.Calendar{UserID: req.user.ID, UID: seg, Name: seg, Color: palette[len(own)%len(palette)]}
	var prop *xnode
	if body != nil {
		prop = body.child("set").child("prop")
	}
	if r.Method == "MKCOL" && prop.child("resourcetype").child("calendar") == nil {
		// extended MKCOL (RFC 5689): only calendars live here
		davError(w, http.StatusForbidden, "<d:valid-resourcetype/>")
		return
	}
	if prop != nil {
		if dn := prop.child("displayname"); dn != nil && strings.TrimSpace(dn.Text) != "" {
			c.Name = strings.TrimSpace(dn.Text)
		}
		if p := prop.child("calendar-color"); p != nil {
			if col, ok := davColor(p.Text); ok {
				c.Color = col
			}
		}
		if d := prop.child("calendar-description"); d != nil {
			c.Description = strings.TrimSpace(d.Text)
		}
		if o := prop.child("calendar-order"); o != nil {
			if v, err := strconv.ParseFloat(strings.TrimSpace(o.Text), 64); err == nil {
				c.SortOrder = v
			}
		}
		if tz := prop.child("calendar-timezone-id"); tz != nil {
			c.Timezone = davZoneID(tz.Text)
		} else if tz := prop.child("calendar-timezone"); tz != nil {
			c.Timezone = davZoneFromVTimezone(tz.Text)
		}
		if comp := prop.child("supported-calendar-component-set"); comp != nil && len(comp.Nodes) > 0 {
			events := false
			for _, cn := range comp.Nodes {
				if strings.EqualFold(cn.attr("name"), "VEVENT") {
					events = true
				}
			}
			if !events {
				davError(w, http.StatusForbidden, "<cal:supported-calendar-component/>")
				return
			}
		}
	}
	if err := s.store.CreateCalendar(ctx, c); err != nil {
		if errors.Is(err, store.ErrConflict) {
			davError(w, http.StatusMethodNotAllowed, "<d:resource-must-be-null/>")
			return
		}
		s.davFail(w, err, "create calendar")
		return
	}
	cid := c.ID
	s.store.AddActivity(ctx, req.user.ID, &cid, nil, "calendar_created", c.Name, "caldav")
	s.touch()
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) davDeleteCollection(ctx context.Context, w http.ResponseWriter, req *davReq, c *davColl) {
	uid := req.user.ID
	switch {
	case c.kind == "invitations":
		http.Error(w, "the Invitations calendar cannot be deleted", http.StatusForbidden)
	case c.role == store.RoleOwner:
		if own, _ := s.store.OwnCalendars(ctx, uid); len(own) <= 1 {
			http.Error(w, "you need at least one calendar", http.StatusForbidden)
			return
		}
		if err := s.store.DeleteCalendar(ctx, c.cal.ID); err != nil {
			s.davFail(w, err, "delete calendar")
			return
		}
		s.store.AddActivity(ctx, uid, nil, nil, "calendar_deleted", c.cal.Name, "caldav")
		s.touch()
		w.WriteHeader(http.StatusNoContent)
	default:
		// deleting someone else's calendar from a phone leaves it, as on the web
		if err := s.store.LeaveCalendar(ctx, uid, c.cal.ID); err != nil {
			s.davFail(w, err, "leave calendar")
			return
		}
		_ = s.store.SetUserSetting(ctx, uid, "left."+itoa64(c.cal.ID), "1")
		s.touch()
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeProppatch(w http.ResponseWriter, href string, ok, failed []string) {
	ms := newMultistatus()
	ms.b.WriteString("<d:response><d:href>" + xesc(hrefEsc(href)) + "</d:href>")
	if len(ok) > 0 {
		ms.b.WriteString("<d:propstat><d:prop>" + strings.Join(ok, "") + "</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>")
	}
	if len(failed) > 0 {
		ms.b.WriteString("<d:propstat><d:prop>" + strings.Join(failed, "") + "</d:prop><d:status>HTTP/1.1 403 Forbidden</d:status></d:propstat>")
	}
	ms.b.WriteString("</d:response>\n")
	ms.write(w, "")
}

// davProppatch changes a calendar's name, colour, order, description or
// zone. People the calendar is shared with change their own colour and
// order; the calendar itself needs the manage role.
func (s *Server) davProppatch(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, c *davColl, href string) {
	body, err := readXML(r)
	if err != nil || body == nil {
		http.Error(w, "bad xml", http.StatusBadRequest)
		return
	}
	uid := req.user.ID
	var ok, failed []string
	calChanged := false
	manage := c.kind == "calendar" && store.RoleAtLeast(c.role, store.RoleManage)
	for _, set := range body.children("set") {
		for _, p := range set.propNodes() {
			v := strings.TrimSpace(p.Text)
			accepted := false
			switch p.XMLName.Space + " " + p.XMLName.Local {
			case nsDAV + " displayname":
				switch {
				case c.kind != "calendar":
				case v == "" || v == s.collDisplayName(c):
					accepted = true
				case manage:
					c.cal.Name, calChanged, accepted = v, true, true
				}
			case nsApple + " calendar-color":
				col, valid := davColor(v)
				switch {
				case !valid || c.kind != "calendar":
				case c.role == store.RoleOwner:
					c.cal.Color, calChanged, accepted = col, true, true
					if c.cal.MyColor != "" {
						accepted = s.store.SavePrefs(ctx, uid, c.cal.ID, nil, nil, &col, nil) == nil
					}
				default:
					accepted = s.store.SavePrefs(ctx, uid, c.cal.ID, nil, nil, &col, nil) == nil
				}
			case nsApple + " calendar-order":
				if n, err := strconv.ParseFloat(v, 64); err == nil && c.kind == "calendar" {
					accepted = s.store.SavePrefs(ctx, uid, c.cal.ID, nil, nil, nil, &n) == nil
				}
			case nsCalDAV + " calendar-description":
				switch {
				case c.kind != "calendar":
				case v == c.cal.Description:
					accepted = true
				case manage:
					c.cal.Description, calChanged, accepted = v, true, true
				}
			case nsCalDAV + " calendar-timezone", nsCalDAV + " calendar-timezone-id":
				z := davZoneID(v)
				if p.XMLName.Local == "calendar-timezone" {
					z = davZoneFromVTimezone(v)
				}
				switch {
				case c.kind != "calendar" || z == "":
				case z == c.cal.Timezone:
					accepted = true
				case manage:
					c.cal.Timezone, calChanged, accepted = z, true, true
				}
			case nsOC + " calendar-enabled", nsCalDAV + " schedule-calendar-transp", nsApple + " refreshrate":
				accepted = true // taken, nothing kept
			}
			if accepted {
				ok = append(ok, propTag(p))
			} else {
				failed = append(failed, propTag(p))
			}
		}
	}
	for _, rm := range body.children("remove") {
		for _, p := range rm.propNodes() {
			failed = append(failed, propTag(p))
		}
	}
	if calChanged {
		if err := s.store.UpdateCalendar(ctx, c.cal); err != nil {
			s.davFail(w, err, "update calendar")
			return
		}
	}
	if len(ok) > 0 {
		s.touch()
	}
	writeProppatch(w, href, ok, failed)
}

// davColor reads "#RRGGBB" or Apple's "#RRGGBBAA".
func davColor(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if len(v) == 9 && v[0] == '#' {
		v = v[:7]
	}
	if len(v) != 7 || !validColor(v) {
		return "", false
	}
	return strings.ToLower(v), true
}

func davZoneID(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if _, err := ical.LoadZone(v); err != nil {
		return ""
	}
	return v
}

func davZoneFromVTimezone(v string) string {
	cal, err := ical.Parse(strings.TrimSpace(v))
	if err != nil {
		return ""
	}
	tz := cal
	if cal.Name != "VTIMEZONE" {
		tz = cal.Child("VTIMEZONE")
	}
	if tz == nil {
		return ""
	}
	return davZoneID(tz.Value("TZID"))
}

// vtimezoneICS describes a zone as a VCALENDAR holding its VTIMEZONE with
// this year's transitions as yearly rules.
func vtimezoneICS(name string) string {
	loc, err := ical.LoadZone(name)
	if err != nil {
		return ""
	}
	type transition struct {
		at       time.Time
		from, to int
	}
	start := time.Date(time.Now().Year(), 1, 1, 0, 0, 0, 0, loc)
	_, prev := start.Zone()
	first := prev
	var trs []transition
	for d := 1; d <= 366; d++ {
		t := start.Add(time.Duration(d) * 24 * time.Hour)
		if _, off := t.Zone(); off != prev {
			lo, hi := t.Add(-24*time.Hour), t
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, o := mid.Zone(); o == prev {
					lo = mid
				} else {
					hi = mid
				}
			}
			trs = append(trs, transition{hi.Truncate(time.Second), prev, off})
			prev = off
		}
	}
	offset := func(sec int) string {
		sign := "+"
		if sec < 0 {
			sign, sec = "-", -sec
		}
		return fmt.Sprintf("%s%02d%02d", sign, sec/3600, sec%3600/60)
	}
	tz := &ical.Component{Name: "VTIMEZONE"}
	tz.SetRaw("TZID", name)
	if len(trs) == 0 {
		std := &ical.Component{Name: "STANDARD"}
		std.SetRaw("DTSTART", "19700101T000000")
		std.SetRaw("TZOFFSETFROM", offset(first))
		std.SetRaw("TZOFFSETTO", offset(first))
		if abbr, _ := start.Zone(); abbr != "" {
			std.SetText("TZNAME", abbr)
		}
		tz.Children = append(tz.Children, std)
	}
	days := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	for _, x := range trs {
		kind := "STANDARD"
		if x.to > x.from {
			kind = "DAYLIGHT"
		}
		wall := x.at.In(time.FixedZone("", x.from))
		nth := (wall.Day()-1)/7 + 1
		if wall.Day()+7 > time.Date(wall.Year(), wall.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			nth = -1
		}
		obs := &ical.Component{Name: kind}
		obs.SetRaw("DTSTART", wall.Format("20060102T150405"))
		obs.SetRaw("RRULE", fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", int(wall.Month()), nth, days[wall.Weekday()]))
		obs.SetRaw("TZOFFSETFROM", offset(x.from))
		obs.SetRaw("TZOFFSETTO", offset(x.to))
		if abbr, _ := x.at.In(loc).Zone(); abbr != "" {
			obs.SetText("TZNAME", abbr)
		}
		tz.Children = append(tz.Children, obs)
	}
	cal := ical.NewCalendar()
	cal.Children = append(cal.Children, tz)
	return cal.String()
}

// ---------- REPORT ----------

func (s *Server) davReport(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, c *davColl, coll *davNode) {
	body, err := readXML(r)
	if err != nil || body == nil {
		http.Error(w, "bad xml", http.StatusBadRequest)
		return
	}
	want, all := requestedProps(body)
	switch body.XMLName.Local {
	case "calendar-query":
		rows, err := s.collectionRows(ctx, req, c)
		if err != nil {
			s.davFail(w, err, "list events")
			return
		}
		filter := body.child("filter")
		ms := newMultistatus()
		for _, row := range rows {
			n := s.eventNode(req, c, row)
			if filter != nil && !s.matchQuery(ctx, filter, n) {
				continue
			}
			ms.response(n.href, s.props(ctx, n, want, all))
		}
		ms.write(w, "")
	case "calendar-multiget":
		var invited map[string]*store.EventRow
		if c.kind == "invitations" {
			rows, err := s.invitedRows(ctx, req)
			if err != nil {
				s.davFail(w, err, "list events")
				return
			}
			invited = map[string]*store.EventRow{}
			for _, row := range rows {
				invited[resourceName(c, row)] = row
			}
		}
		ms := newMultistatus()
		for _, h := range body.children("href") {
			raw := strings.TrimSpace(h.Text)
			p := raw
			if u, err := url.Parse(raw); err == nil && u.Path != "" {
				p = u.Path
			}
			name := path.Base(p)
			var row *store.EventRow
			if path.Base(path.Dir(p)) == c.seg {
				if c.kind == "invitations" {
					row = invited[name]
				} else if e, err := s.store.GetEventByHref(ctx, c.cal.ID, name); err == nil {
					row = e
				}
			}
			if row == nil {
				ms.goneRaw(raw)
				continue
			}
			ms.responseRaw(raw, s.props(ctx, s.eventNode(req, c, row), want, all))
		}
		ms.write(w, "")
	case "sync-collection":
		token := strings.TrimSpace(body.child("sync-token").textOr(""))
		if c.kind == "invitations" {
			s.davSyncInvitations(ctx, w, req, c, coll, token, want, all)
			return
		}
		s.davSyncCalendar(ctx, w, req, c, token, want, all)
	default:
		davError(w, http.StatusForbidden, "<d:supported-report/>")
	}
}

// davSyncCalendar answers sync-collection (RFC 6578) from the change
// numbers events and tombstones carry.
func (s *Server) davSyncCalendar(ctx context.Context, w http.ResponseWriter, req *davReq, c *davColl, token string, want []propName, all bool) {
	since := int64(0)
	if token != "" {
		n, err := strconv.ParseInt(strings.TrimPrefix(token, syncPfx), 10, 64)
		if err != nil || !strings.HasPrefix(token, syncPfx) || n < 0 {
			davError(w, http.StatusForbidden, "<d:valid-sync-token/>")
			return
		}
		// deletes from before the oldest tombstone may be forgotten: start over
		if oldest := s.store.OldestTombstone(ctx, c.cal.ID); oldest > 0 && n < oldest-1 {
			davError(w, http.StatusForbidden, "<d:valid-sync-token/>")
			return
		}
		since = n
	}
	// the token handed out is read before the changes, so nothing slips between
	cur, err := s.store.GetCalendar(ctx, c.cal.ID)
	if err != nil {
		s.davFail(w, err, "read calendar")
		return
	}
	changes, err := s.store.ChangesSince(ctx, c.cal.ID, since)
	if err != nil {
		s.davFail(w, err, "sync")
		return
	}
	ms := newMultistatus()
	base := s.homeHref(req) + c.seg + "/"
	for _, ch := range changes {
		var row *store.EventRow
		if !ch.Deleted {
			row, _ = s.store.GetEventByHref(ctx, c.cal.ID, ch.Href)
		}
		if row == nil {
			// a first sync lists what is there, not what went
			if token != "" {
				ms.gone(base + ch.Href)
			}
			continue
		}
		n := s.eventNode(req, c, row)
		ms.response(n.href, s.props(ctx, n, want, all))
	}
	newSeq := max(cur.Seq, since)
	ms.write(w, "<d:sync-token>"+xesc(syncPfx+strconv.FormatInt(newSeq, 10))+"</d:sync-token>\n")
}

// davSyncInvitations answers sync-collection for Invitations: nothing when
// the token is current, everything for a first sync, and "start over" for
// an older token (which events left the collection is not remembered).
func (s *Server) davSyncInvitations(ctx context.Context, w http.ResponseWriter, req *davReq, c *davColl, coll *davNode, token string, want []propName, all bool) {
	cur := syncPfx + "inv-" + coll.ctag
	ms := newMultistatus()
	switch token {
	case cur:
	case "":
		rows, err := s.invitedRows(ctx, req)
		if err != nil {
			s.davFail(w, err, "list events")
			return
		}
		for _, row := range rows {
			n := s.eventNode(req, c, row)
			ms.response(n.href, s.props(ctx, n, want, all))
		}
	default:
		davError(w, http.StatusForbidden, "<d:valid-sync-token/>")
		return
	}
	ms.write(w, "<d:sync-token>"+xesc(cur)+"</d:sync-token>\n")
}

// matchQuery evaluates a calendar-query filter against an event.
func (s *Server) matchQuery(ctx context.Context, filter *xnode, n *davNode) bool {
	vcal := filter.child("comp-filter")
	if vcal == nil {
		return true
	}
	if name := vcal.attr("name"); name != "" && !strings.EqualFold(name, "VCALENDAR") {
		return false
	}
	if vcal.child("is-not-defined") != nil {
		return false
	}
	for _, cf := range vcal.children("comp-filter") {
		if !s.matchComp(ctx, cf, n) {
			return false
		}
	}
	return true
}

func (s *Server) matchComp(ctx context.Context, cf *xnode, n *davNode) bool {
	notDefined := cf.child("is-not-defined") != nil
	if !strings.EqualFold(cf.attr("name"), "VEVENT") {
		// VTODO, VJOURNAL, …: never here
		return notDefined
	}
	if notDefined {
		return false
	}
	if tr := cf.child("time-range"); tr != nil && !s.occursIn(ctx, n.row, tr) {
		return false
	}
	pfs := cf.children("prop-filter")
	if len(pfs) == 0 {
		return true
	}
	// properties are matched on what the viewer sees (masked events stay masked)
	cal, err := ical.Parse(n.ics)
	if err != nil {
		return false
	}
	events := cal.ChildrenNamed("VEVENT")
	loc := s.calLoc(ctx, n.row.CalendarID)
	for _, pf := range pfs {
		if !matchProp(pf, events, loc) {
			return false
		}
	}
	return true
}

func parseRangeTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"20060102T150405Z", "20060102T150405", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// occursIn reports whether any occurrence of the event overlaps the
// time-range (recurring series are expanded).
func (s *Server) occursIn(ctx context.Context, row *store.EventRow, tr *xnode) bool {
	from, ok := parseRangeTime(tr.attr("start"))
	if !ok {
		from = time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	to, ok := parseRangeTime(tr.attr("end"))
	if !ok {
		to = ical.Forever
	}
	if !to.After(from) {
		return false
	}
	// the index columns rule most events out without parsing them
	if row.RangeStart != "" && row.RangeEnd != "" &&
		(row.RangeStart >= to.UTC().Format(store.RangeLayout) || row.RangeEnd <= from.UTC().Format(store.RangeLayout)) {
		return false
	}
	res, err := s.resource(ctx, row)
	if err != nil {
		return false
	}
	return len(res.Expand(from, to)) > 0
}

func matchProp(pf *xnode, events []*ical.Component, loc *time.Location) bool {
	name := strings.ToUpper(pf.attr("name"))
	var props []ical.Property
	for _, ev := range events {
		props = append(props, ev.GetAll(name)...)
	}
	if pf.child("is-not-defined") != nil {
		return len(props) == 0
	}
	if len(props) == 0 {
		return false
	}
	if tm := pf.child("text-match"); tm != nil {
		neg := strings.EqualFold(tm.attr("negate-condition"), "yes")
		needle := strings.ToLower(strings.TrimSpace(tm.Text))
		hit := false
		for i := range props {
			if strings.Contains(strings.ToLower(props[i].Text()), needle) {
				hit = true
				break
			}
		}
		if hit == neg {
			return false
		}
	}
	if tr := pf.child("time-range"); tr != nil {
		t, ok := ical.ReadUTC(&props[0], loc)
		if !ok {
			return false
		}
		if start, ok := parseRangeTime(tr.attr("start")); ok && t.Before(start) {
			return false
		}
		if end, ok := parseRangeTime(tr.attr("end")); ok && !t.Before(end) {
			return false
		}
	}
	return true
}

// ---------- resources ----------

func (s *Server) davResource(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, seg, name string) {
	c, err := s.resolveCollection(ctx, req, seg)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.davNotFound(w)
		} else {
			s.davFail(w, err, "list calendars")
		}
		return
	}
	var row *store.EventRow
	if c.kind == "invitations" {
		rows, err := s.invitedRows(ctx, req)
		if err != nil {
			s.davFail(w, err, "list events")
			return
		}
		for _, e := range rows {
			if resourceName(c, e) == name {
				row = e
			}
		}
	} else {
		row, err = s.store.GetEventByHref(ctx, c.cal.ID, name)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.davFail(w, err, "read event")
			return
		}
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if row == nil {
			s.davNotFound(w)
			return
		}
		n := s.eventNode(req, c, row)
		w.Header().Set("ETag", n.etag)
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(n.ics)))
		if t := store.ParseTime(row.UpdatedAt); !t.IsZero() {
			w.Header().Set("Last-Modified", t.UTC().Format(http.TimeFormat))
		}
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(n.ics))
	case "PROPFIND":
		if row == nil {
			s.davNotFound(w)
			return
		}
		body, err := readXML(r)
		if err != nil {
			http.Error(w, "bad xml", http.StatusBadRequest)
			return
		}
		want, all := requestedProps(body)
		n := s.eventNode(req, c, row)
		ms := newMultistatus()
		ms.response(n.href, s.props(ctx, n, want, all))
		ms.write(w, "")
	case http.MethodPut:
		s.davPut(ctx, w, r, req, c, name, row)
	case http.MethodDelete:
		s.davDelete(ctx, w, r, req, c, row)
	default:
		s.davMethodNotAllowed(w)
	}
}

func (s *Server) davDelete(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, c *davColl, row *store.EventRow) {
	if row == nil {
		s.davNotFound(w)
		return
	}
	if !c.writable() {
		davError(w, http.StatusForbidden, "<d:need-privileges/>")
		return
	}
	ifMatch := ""
	if im := r.Header.Get("If-Match"); im != "" && im != "*" {
		if !etagMatches(im, row.ETag) {
			http.Error(w, "the event changed meanwhile", http.StatusPreconditionFailed)
			return
		}
		ifMatch = row.ETag
	}
	err := s.store.DeleteEvent(ctx, row, store.DeleteEventOpts{Actor: req.user.ID, KeepCopy: true, IfMatch: ifMatch},
		s.eventHook(ctx, "deleted", c.cal, row.UID, row.Summary, "caldav"))
	if errors.Is(err, store.ErrPrecondition) {
		http.Error(w, "the event changed meanwhile", http.StatusPreconditionFailed)
		return
	}
	if err != nil {
		s.davFail(w, err, "delete event")
		return
	}
	s.afterDelete(ctx, req.user, row)
	w.WriteHeader(http.StatusNoContent)
}

// davPut stores one calendar object: a VCALENDAR with the VEVENTs of a
// single UID (the master and its RECURRENCE-ID overrides).
func (s *Server) davPut(ctx context.Context, w http.ResponseWriter, r *http.Request, req *davReq, c *davColl, name string, row *store.EventRow) {
	if !c.writable() {
		davError(w, http.StatusForbidden, "<d:need-privileges/>")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxICSBytes+1))
	if err != nil {
		http.Error(w, "could not read the body", http.StatusBadRequest)
		return
	}
	if len(body) > maxICSBytes {
		davError(w, http.StatusRequestEntityTooLarge, "<cal:max-resource-size/>")
		return
	}
	cal, err := ical.Parse(string(body))
	if err != nil || cal.Name != "VCALENDAR" {
		davError(w, http.StatusBadRequest, "<cal:valid-calendar-data/>")
		return
	}
	var events []*ical.Component
	for _, ch := range cal.Children {
		switch ch.Name {
		case "VEVENT":
			events = append(events, ch)
		case "VTODO", "VJOURNAL", "VFREEBUSY", "VAVAILABILITY":
			davError(w, http.StatusForbidden, "<cal:supported-calendar-component/>")
			return
		}
	}
	if len(events) == 0 {
		davError(w, http.StatusForbidden, "<cal:supported-calendar-component/>")
		return
	}
	uids := map[string]bool{}
	for _, ev := range events {
		if u := strings.TrimSpace(ev.Value("UID")); u != "" {
			uids[u] = true
		}
	}
	if len(uids) > 1 {
		davError(w, http.StatusBadRequest, "<cal:valid-calendar-object-resource/><d:responsedescription>one event per resource: this object holds "+
			strconv.Itoa(len(uids))+" UIDs</d:responsedescription>")
		return
	}
	ics := string(body)
	rewritten := false
	if len(uids) == 0 {
		uid := strings.TrimSuffix(name, ".ics")
		for _, ev := range events {
			ev.SetText("UID", uid)
		}
		ics, rewritten = cal.String(), true
	}
	loc := s.calLoc(ctx, c.cal.ID)
	uid, err := store.ValidateICS(ics, loc)
	if err != nil {
		davError(w, http.StatusForbidden, "<cal:valid-calendar-object-resource/><d:responsedescription>"+xesc(err.Error())+"</d:responsedescription>")
		return
	}
	ifMatch := ""
	if im := strings.TrimSpace(r.Header.Get("If-Match")); im != "" {
		if im == "*" {
			ifMatch = "*"
		} else {
			if row == nil || !etagMatches(im, row.ETag) {
				http.Error(w, "the event changed meanwhile", http.StatusPreconditionFailed)
				return
			}
			ifMatch = row.ETag
		}
	}
	ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match")) == "*"
	if ifNoneMatch && row != nil {
		http.Error(w, "there is an event here already", http.StatusPreconditionFailed)
		return
	}
	var before *ical.Resource
	kind := "created"
	if row != nil {
		before, _ = ical.ParseResource(row.ICS, loc)
		kind = "updated"
	}
	summary := ""
	if res, err := ical.ParseResource(ics, loc); err == nil {
		summary = res.MasterEvent().Summary
	}
	saved, created, err := s.store.SaveEvent(ctx, store.EventWrite{CalendarID: c.cal.ID, Href: name, ICS: ics, Loc: loc,
		Source: "caldav", Actor: req.user.ID, IfMatch: ifMatch, IfNoneMatch: ifNoneMatch},
		s.eventHook(ctx, kind, c.cal, uid, summary, "caldav"))
	switch {
	case errors.Is(err, store.ErrPrecondition):
		http.Error(w, "the event changed meanwhile", http.StatusPreconditionFailed)
		return
	case errors.Is(err, store.ErrUIDTaken):
		cond := "<cal:no-uid-conflict/>"
		if other, e := s.store.GetEventByUID(ctx, c.cal.ID, uid); e == nil {
			cond = "<cal:no-uid-conflict>" + hrefXML(s.homeHref(req)+c.seg+"/"+other.Href) + "</cal:no-uid-conflict>"
		}
		davError(w, http.StatusConflict, cond)
		return
	case errors.Is(err, store.ErrConflict):
		http.Error(w, "conflict", http.StatusConflict)
		return
	case err != nil:
		s.davFail(w, err, "store event")
		return
	}
	s.afterWrite(ctx, req.user, before, saved, created)
	if !rewritten {
		// the stored text is what the client sent, so its copy is current
		w.Header().Set("ETag", saved.ETag)
	}
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if part == "*" || part == etag || part == strings.Trim(etag, `"`) || `"`+part+`"` == etag {
			return true
		}
	}
	return false
}
