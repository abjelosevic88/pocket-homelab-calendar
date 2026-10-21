package ical

import (
	"strconv"
	"strings"
	"time"
)

// Alarm is one VALARM in the shape the API uses: a trigger relative to the
// start such as "-PT15M", or an absolute UTC moment "2026-10-21T08:00:00Z".
type Alarm struct {
	Trigger     string `json:"trigger"`
	Description string `json:"description,omitempty"`
}

// Attendee is an ATTENDEE (or the ORGANIZER) of an event.
type Attendee struct {
	Email    string `json:"email"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role,omitempty"`   // REQ-PARTICIPANT | OPT-PARTICIPANT | CHAIR | NON-PARTICIPANT
	Status   string `json:"status,omitempty"` // NEEDS-ACTION | ACCEPTED | DECLINED | TENTATIVE | DELEGATED
	RSVP     bool   `json:"rsvp,omitempty"`
	Resource bool   `json:"resource,omitempty"` // CUTYPE=ROOM/RESOURCE
}

// Event is what the index and the API keep of a VEVENT.
type Event struct {
	UID         string
	Summary     string
	Description string
	Location    string
	URL         string
	Conference  string // video call link (X-GOOGLE-CONFERENCE, CONFERENCE, or one found in the text)
	Status      string // confirmed | tentative | cancelled
	Transp      string // opaque | transparent
	Class       string // public | private | confidential
	Color       string
	Categories  []string

	AllDay bool
	Start  time.Time // in Zone (all-day: midnight in Zone)
	End    time.Time // exclusive
	Zone   *time.Location
	TZID   string // "" = floating or UTC

	RRule         string
	ExDates       []time.Time
	RDates        []time.Time
	RecurrenceID  time.Time // set on an override
	ThisAndFuture bool

	Alarms    []Alarm
	Organizer *Attendee
	Attendees []Attendee

	Sequence int
	Created  time.Time
	Modified time.Time
}

// Status, transparency and class values.
const (
	StatusConfirmed = "confirmed"
	StatusTentative = "tentative"
	StatusCancelled = "cancelled"
)

// managed are the VEVENT properties this app rewrites; every other one is
// left exactly as the client that wrote it sent it.
var managed = map[string]bool{
	"SUMMARY": true, "DESCRIPTION": true, "LOCATION": true, "URL": true, "STATUS": true, "TRANSP": true,
	"CLASS": true, "COLOR": true, "CATEGORIES": true, "DTSTART": true, "DTEND": true, "DURATION": true,
	"RRULE": true, "EXDATE": true, "RDATE": true, "ORGANIZER": true, "ATTENDEE": true,
	"LAST-MODIFIED": true, "DTSTAMP": true, "SEQUENCE": true, "X-POCKET-CONFERENCE": true,
}

// ReadEvent maps a VEVENT onto Event. loc is the zone floating times are read in.
func ReadEvent(c *Component, loc *time.Location) *Event {
	e := &Event{
		UID:         c.Value("UID"),
		Summary:     c.Text("SUMMARY"),
		Description: c.Text("DESCRIPTION"),
		Location:    c.Text("LOCATION"),
		URL:         c.Value("URL"),
		Status:      strings.ToLower(c.Value("STATUS")),
		Transp:      strings.ToLower(c.Value("TRANSP")),
		Class:       strings.ToLower(c.Value("CLASS")),
		Color:       c.Value("COLOR"),
		Zone:        loc,
	}
	switch e.Status {
	case StatusTentative, StatusCancelled:
	default:
		e.Status = StatusConfirmed
	}
	if e.Transp != "transparent" {
		e.Transp = "opaque"
	}
	if e.Class != "private" && e.Class != "confidential" {
		e.Class = "public"
	}
	for _, p := range c.GetAll("CATEGORIES") {
		for _, v := range splitParamValues(p.Value) {
			if v = strings.TrimSpace(Unescape(v)); v != "" {
				e.Categories = append(e.Categories, v)
			}
		}
	}
	if st := c.Get("DTSTART"); st != nil {
		e.Start, e.AllDay, e.Zone, e.TZID = readMoment(st, loc)
	}
	if en := c.Get("DTEND"); en != nil {
		e.End, _, _, _ = readMoment(en, loc)
		if !e.Start.IsZero() && !e.End.IsZero() {
			e.End = e.End.In(e.Zone)
		}
	} else if d := c.Value("DURATION"); d != "" {
		if dur, err := Duration(d); err == nil && !e.Start.IsZero() {
			e.End = addDuration(e.Start, dur, e.AllDay)
		}
	}
	if !e.Start.IsZero() && (e.End.IsZero() || e.End.Before(e.Start)) {
		if e.AllDay {
			e.End = e.Start.AddDate(0, 0, 1)
		} else {
			e.End = e.Start
		}
	}
	if e.AllDay && !e.End.After(e.Start) {
		e.End = e.Start.AddDate(0, 0, 1)
	}
	e.RRule = strings.TrimPrefix(c.Value("RRULE"), "RRULE:")
	for _, p := range c.GetAll("EXDATE") {
		e.ExDates = append(e.ExDates, readList(&p, e.Zone)...)
	}
	for _, p := range c.GetAll("RDATE") {
		if strings.EqualFold(p.Param("VALUE"), "PERIOD") {
			continue
		}
		e.RDates = append(e.RDates, readList(&p, e.Zone)...)
	}
	if rid := c.Get("RECURRENCE-ID"); rid != nil {
		e.RecurrenceID, _, _, _ = readMoment(rid, e.Zone)
		e.ThisAndFuture = strings.EqualFold(rid.Param("RANGE"), "THISANDFUTURE")
	}
	if o := c.Get("ORGANIZER"); o != nil {
		a := readAttendee(o)
		e.Organizer = &a
	}
	for _, p := range c.GetAll("ATTENDEE") {
		e.Attendees = append(e.Attendees, readAttendee(&p))
	}
	for _, a := range c.ChildrenNamed("VALARM") {
		tr := a.Get("TRIGGER")
		if tr == nil {
			continue
		}
		trig := tr.Value
		if strings.EqualFold(tr.Param("VALUE"), "DATE-TIME") || strings.HasSuffix(trig, "Z") {
			if t, ok := ReadUTC(tr, e.Zone); ok {
				trig = t.UTC().Format(time.RFC3339)
			}
		} else if strings.EqualFold(tr.Param("RELATED"), "END") {
			// keep it relative to the start so the API has one shape
			if d, err := Duration(trig); err == nil {
				trig = FormatDuration(d + e.End.Sub(e.Start))
			}
		}
		e.Alarms = append(e.Alarms, Alarm{Trigger: trig, Description: a.Text("DESCRIPTION")})
	}
	e.Conference = c.Value("X-POCKET-CONFERENCE")
	if e.Conference == "" {
		e.Conference = firstNonEmptyProp(c, "X-GOOGLE-CONFERENCE", "X-MICROSOFT-SKYPETEAMSMEETINGURL")
	}
	if e.Conference == "" {
		if conf := c.Get("CONFERENCE"); conf != nil {
			e.Conference = conf.Value
		}
	}
	if e.Conference == "" {
		e.Conference = FindMeetingLink(e.Location + "\n" + e.Description + "\n" + e.URL)
	}
	e.Sequence, _ = strconv.Atoi(c.Value("SEQUENCE"))
	e.Created, _ = ReadUTC(c.Get("CREATED"), time.UTC)
	e.Modified, _ = ReadUTC(c.Get("LAST-MODIFIED"), time.UTC)
	return e
}

func firstNonEmptyProp(c *Component, names ...string) string {
	for _, n := range names {
		if v := c.Value(n); v != "" {
			return v
		}
	}
	return ""
}

var meetingHosts = []string{"meet.google.com/", "zoom.us/j/", "zoom.us/my/", "teams.microsoft.com/l/meetup-join", "teams.live.com/meet", "whereby.com/", "meet.jit.si/", "app.gather.town/", "webex.com/meet", "webex.com/join", "facetime.apple.com/join", "discord.gg/", "talk/"}

// FindMeetingLink returns the first video call link in a text.
func FindMeetingLink(s string) string {
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '<' || r == '>' || r == '"' || r == '(' || r == ')'
	}) {
		if !strings.HasPrefix(f, "https://") {
			continue
		}
		for _, h := range meetingHosts {
			if strings.Contains(f, h) {
				return strings.TrimRight(f, ".,;")
			}
		}
	}
	return ""
}

func readAttendee(p *Property) Attendee {
	email := p.Value
	if len(email) > 7 && strings.EqualFold(email[:7], "mailto:") {
		email = email[7:]
	}
	cut := strings.ToUpper(p.Param("CUTYPE"))
	return Attendee{
		Email:    strings.ToLower(strings.TrimSpace(email)),
		Name:     strings.Trim(p.Param("CN"), `"`),
		Role:     strings.ToUpper(p.Param("ROLE")),
		Status:   strings.ToUpper(p.Param("PARTSTAT")),
		RSVP:     strings.EqualFold(p.Param("RSVP"), "TRUE"),
		Resource: cut == "ROOM" || cut == "RESOURCE",
	}
}

func attendeeProperty(name string, a Attendee) Property {
	p := Property{Name: name, Value: "mailto:" + a.Email}
	if a.Name != "" {
		p.SetParam("CN", a.Name)
	}
	if name == "ATTENDEE" {
		role := a.Role
		if role == "" {
			role = "REQ-PARTICIPANT"
		}
		p.SetParam("ROLE", role)
		st := a.Status
		if st == "" {
			st = "NEEDS-ACTION"
		}
		p.SetParam("PARTSTAT", st)
		if a.RSVP || st == "NEEDS-ACTION" {
			p.SetParam("RSVP", "TRUE")
		}
		if a.Resource {
			p.SetParam("CUTYPE", "RESOURCE")
		}
	}
	return p
}

// readMoment reads DTSTART/DTEND/RECURRENCE-ID into an instant in its zone.
func readMoment(p *Property, loc *time.Location) (time.Time, bool, *time.Location, string) {
	v := strings.TrimSpace(p.Value)
	if strings.EqualFold(p.Param("VALUE"), "DATE") || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, loc)
		if err != nil {
			return time.Time{}, true, loc, ""
		}
		return t, true, loc, ""
	}
	utc := strings.HasSuffix(v, "Z")
	v = strings.TrimSuffix(v, "Z")
	layout := "20060102T150405"
	if len(v) == 13 {
		layout = "20060102T1504"
	}
	switch {
	case utc:
		t, err := time.Parse(layout, v)
		if err != nil {
			return time.Time{}, false, loc, ""
		}
		return t.In(loc), false, loc, ""
	case p.Param("TZID") != "":
		name := cleanTZID(p.Param("TZID"))
		z, err := LoadZone(name)
		if err != nil {
			z, name = loc, ""
		}
		t, err := time.ParseInLocation(layout, v, z)
		if err != nil {
			return time.Time{}, false, z, name
		}
		return t, false, z, name
	default:
		t, err := time.ParseInLocation(layout, v, loc)
		if err != nil {
			return time.Time{}, false, loc, ""
		}
		return t, false, loc, ""
	}
}

func readList(p *Property, loc *time.Location) []time.Time {
	var out []time.Time
	for _, v := range strings.Split(p.Value, ",") {
		q := Property{Name: p.Name, Params: p.Params, Value: strings.TrimSpace(v)}
		if t, _, _, _ := readMoment(&q, loc); !t.IsZero() {
			out = append(out, t)
		}
	}
	return out
}

func addDuration(t time.Time, d time.Duration, allDay bool) time.Time {
	if allDay {
		days := int(d / (24 * time.Hour))
		if days < 1 {
			days = 1
		}
		return t.AddDate(0, 0, days)
	}
	return t.Add(d)
}

// windowsZones maps the Windows zone names Outlook and Exchange write as
// TZID onto IANA names (the common ones; anything else falls back).
var windowsZones = map[string]string{
	"W. Europe Standard Time": "Europe/Berlin", "Central Europe Standard Time": "Europe/Budapest",
	"Central European Standard Time": "Europe/Warsaw", "Romance Standard Time": "Europe/Paris",
	"GMT Standard Time": "Europe/London", "Greenwich Standard Time": "Atlantic/Reykjavik",
	"E. Europe Standard Time": "Europe/Chisinau", "FLE Standard Time": "Europe/Kiev", "GTB Standard Time": "Europe/Bucharest",
	"Russian Standard Time": "Europe/Moscow", "Turkey Standard Time": "Europe/Istanbul",
	"Eastern Standard Time": "America/New_York", "Central Standard Time": "America/Chicago",
	"Mountain Standard Time": "America/Denver", "Pacific Standard Time": "America/Los_Angeles",
	"US Mountain Standard Time": "America/Phoenix", "Alaskan Standard Time": "America/Anchorage",
	"Hawaiian Standard Time": "Pacific/Honolulu", "Atlantic Standard Time": "America/Halifax",
	"China Standard Time": "Asia/Shanghai", "Tokyo Standard Time": "Asia/Tokyo", "India Standard Time": "Asia/Kolkata",
	"Singapore Standard Time": "Asia/Singapore", "AUS Eastern Standard Time": "Australia/Sydney",
	"Arabian Standard Time": "Asia/Dubai", "UTC": "UTC", "Coordinated Universal Time": "UTC",
}

// LoadZone loads an IANA or Windows zone name.
func LoadZone(name string) (*time.Location, error) {
	if w, ok := windowsZones[name]; ok {
		name = w
	}
	return time.LoadLocation(name)
}

// ---------- writing ----------

// EventInput is a change to apply to a VEVENT; nil fields stay as they are.
type EventInput struct {
	Summary     *string
	Description *string
	Location    *string
	URL         *string
	Conference  *string
	Status      *string
	Transp      *string
	Class       *string
	Color       *string
	Categories  *[]string
	AllDay      bool
	Start       *time.Time // in the event's zone (all-day: the date at midnight)
	End         *time.Time
	TZID        *string
	RRule       *string
	ExDates     *[]time.Time
	Alarms      *[]Alarm
	Organizer   *Attendee
	Attendees   *[]Attendee
}

// ApplyEvent writes in onto the VEVENT c (keeping every property it does not
// manage) and stamps LAST-MODIFIED, DTSTAMP and SEQUENCE.
func ApplyEvent(c *Component, in EventInput) {
	if in.Summary != nil {
		setOrRemoveText(c, "SUMMARY", *in.Summary)
	}
	if in.Description != nil {
		setOrRemoveText(c, "DESCRIPTION", *in.Description)
	}
	if in.Location != nil {
		setOrRemoveText(c, "LOCATION", *in.Location)
	}
	if in.URL != nil {
		setOrRemoveRaw(c, "URL", *in.URL)
	}
	if in.Conference != nil {
		setOrRemoveRaw(c, "X-POCKET-CONFERENCE", *in.Conference)
	}
	if in.Status != nil {
		switch *in.Status {
		case StatusTentative, StatusCancelled:
			c.SetRaw("STATUS", strings.ToUpper(*in.Status))
		default:
			c.SetRaw("STATUS", "CONFIRMED")
		}
	}
	if in.Transp != nil {
		if *in.Transp == "transparent" {
			c.SetRaw("TRANSP", "TRANSPARENT")
		} else {
			c.SetRaw("TRANSP", "OPAQUE")
		}
	}
	if in.Class != nil {
		switch *in.Class {
		case "private", "confidential":
			c.SetRaw("CLASS", strings.ToUpper(*in.Class))
		default:
			c.Remove("CLASS")
		}
	}
	if in.Color != nil {
		setOrRemoveRaw(c, "COLOR", *in.Color)
	}
	if in.Categories != nil {
		c.Remove("CATEGORIES")
		if len(*in.Categories) > 0 {
			vals := make([]string, 0, len(*in.Categories))
			for _, v := range *in.Categories {
				vals = append(vals, Escape(v))
			}
			c.Set(Property{Name: "CATEGORIES", Value: strings.Join(vals, ",")})
		}
	}
	if in.Start != nil {
		tzid := ""
		if in.TZID != nil {
			tzid = *in.TZID
		}
		c.Set(MomentProperty("DTSTART", *in.Start, in.AllDay, tzid))
		if in.End != nil {
			c.Remove("DURATION")
			c.Set(MomentProperty("DTEND", *in.End, in.AllDay, tzid))
		}
		// keep EXDATEs in the same shape as DTSTART
		if in.ExDates == nil {
			ex := ReadEvent(c, in.Start.Location()).ExDates
			in.ExDates = &ex
		}
	}
	if in.RRule != nil {
		setOrRemoveRaw(c, "RRULE", strings.TrimPrefix(*in.RRule, "RRULE:"))
	}
	if in.ExDates != nil {
		c.Remove("EXDATE")
		st := c.Get("DTSTART")
		allDay, tzid := false, ""
		if st != nil {
			allDay = strings.EqualFold(st.Param("VALUE"), "DATE") || len(st.Value) == 8
			tzid = st.Param("TZID")
		}
		for _, t := range *in.ExDates {
			c.Props = append(c.Props, MomentProperty("EXDATE", t, allDay, tzid))
		}
	}
	if in.Organizer != nil {
		if in.Organizer.Email == "" {
			c.Remove("ORGANIZER")
		} else {
			c.Set(attendeeProperty("ORGANIZER", *in.Organizer))
		}
	}
	if in.Attendees != nil {
		c.Remove("ATTENDEE")
		for _, a := range *in.Attendees {
			if a.Email != "" {
				c.Props = append(c.Props, attendeeProperty("ATTENDEE", a))
			}
		}
	}
	if in.Alarms != nil {
		c.RemoveChildren("VALARM")
		summary := c.Text("SUMMARY")
		for _, a := range *in.Alarms {
			al := &Component{Name: "VALARM"}
			al.SetRaw("ACTION", "DISPLAY")
			if t, err := time.Parse(time.RFC3339, a.Trigger); err == nil {
				al.Set(Property{Name: "TRIGGER", Params: []Param{{Name: "VALUE", Values: []string{"DATE-TIME"}}}, Value: t.UTC().Format("20060102T150405Z")})
			} else {
				al.SetRaw("TRIGGER", a.Trigger)
			}
			desc := a.Description
			if desc == "" {
				desc = summary
			}
			if desc == "" {
				desc = "Reminder"
			}
			al.SetText("DESCRIPTION", desc)
			c.Children = append(c.Children, al)
		}
	}
	Touch(c)
}

// Touch stamps LAST-MODIFIED/DTSTAMP and bumps SEQUENCE.
func Touch(c *Component) {
	now := time.Now().UTC()
	c.Set(UTCProperty("LAST-MODIFIED", now))
	c.Set(UTCProperty("DTSTAMP", now))
	n, _ := strconv.Atoi(c.Value("SEQUENCE"))
	c.SetRaw("SEQUENCE", strconv.Itoa(n+1))
	if c.Get("CREATED") == nil {
		c.Set(UTCProperty("CREATED", now))
	}
}

// MomentProperty writes a DATE, a DATE-TIME with TZID, or a UTC DATE-TIME.
func MomentProperty(name string, t time.Time, allDay bool, tzid string) Property {
	if allDay {
		return Property{Name: name, Params: []Param{{Name: "VALUE", Values: []string{"DATE"}}}, Value: t.Format("20060102")}
	}
	if tzid != "" && tzid != "UTC" {
		if z, err := LoadZone(tzid); err == nil {
			return Property{Name: name, Params: []Param{{Name: "TZID", Values: []string{tzid}}}, Value: t.In(z).Format("20060102T150405")}
		}
	}
	return UTCProperty(name, t)
}

func setOrRemoveText(c *Component, name, v string) {
	if v == "" {
		c.Remove(name)
		return
	}
	c.SetText(name, v)
}

func setOrRemoveRaw(c *Component, name, v string) {
	if v == "" {
		c.Remove(name)
		return
	}
	c.SetRaw(name, v)
}

// NewEvent returns a fresh VEVENT with UID and stamps.
func NewEvent(uid string) *Component {
	c := &Component{Name: "VEVENT"}
	c.SetRaw("UID", uid)
	now := time.Now().UTC()
	c.Set(UTCProperty("DTSTAMP", now))
	c.Set(UTCProperty("CREATED", now))
	c.SetRaw("SEQUENCE", "0")
	return c
}

// RecurrenceKey is the stable identifier of one occurrence: the original
// start as YYYYMMDD (all-day) or YYYYMMDDTHHMMSSZ (UTC).
func RecurrenceKey(t time.Time, allDay bool) string {
	if allDay {
		return t.Format("20060102")
	}
	return t.UTC().Format("20060102T150405Z")
}

// ParseRecurrenceKey reads a RecurrenceKey back (all-day dates in loc).
func ParseRecurrenceKey(k string, loc *time.Location) (time.Time, bool, bool) {
	if len(k) == 8 {
		t, err := time.ParseInLocation("20060102", k, loc)
		return t, true, err == nil
	}
	t, err := time.Parse("20060102T150405Z", k)
	return t, false, err == nil
}
