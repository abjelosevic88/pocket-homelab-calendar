package ical

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/teambition/rrule-go"
)

// Resource is one calendar object: a master VEVENT and its overrides
// (VEVENTs with a RECURRENCE-ID), all sharing one UID.
type Resource struct {
	Cal       *Component
	Master    *Component // nil when the object only carries overrides
	Overrides map[string]*Component
	Loc       *time.Location // floating times are read here
}

// ErrNoEvent means the text holds no VEVENT.
var ErrNoEvent = errors.New("no VEVENT in the calendar object")

// ParseResource reads an iCalendar object holding one event series.
func ParseResource(ics string, loc *time.Location) (*Resource, error) {
	cal, err := Parse(ics)
	if err != nil {
		return nil, err
	}
	return FromCalendar(cal, loc)
}

// FromCalendar indexes the VEVENTs of a parsed VCALENDAR.
func FromCalendar(cal *Component, loc *time.Location) (*Resource, error) {
	if loc == nil {
		loc = time.UTC
	}
	r := &Resource{Cal: cal, Overrides: map[string]*Component{}, Loc: loc}
	for _, ev := range cal.ChildrenNamed("VEVENT") {
		if ev.Get("RECURRENCE-ID") == nil {
			if r.Master == nil {
				r.Master = ev
			}
			continue
		}
		e := ReadEvent(ev, r.zone())
		allDay := e.AllDay
		if p := ev.Get("RECURRENCE-ID"); p != nil {
			allDay = strings.EqualFold(p.Param("VALUE"), "DATE") || len(p.Value) == 8
		}
		r.Overrides[RecurrenceKey(e.RecurrenceID, allDay)] = ev
	}
	if r.Master == nil && len(r.Overrides) == 0 {
		return nil, ErrNoEvent
	}
	return r, nil
}

func (r *Resource) zone() *time.Location {
	if r.Master != nil {
		if st := r.Master.Get("DTSTART"); st != nil {
			_, _, z, _ := readMoment(st, r.Loc)
			if z != nil {
				return z
			}
		}
	}
	return r.Loc
}

// UID returns the series UID.
func (r *Resource) UID() string {
	if r.Master != nil {
		return r.Master.Value("UID")
	}
	for _, o := range r.Overrides {
		return o.Value("UID")
	}
	return ""
}

// MasterEvent reads the master (or the first override when there is none).
func (r *Resource) MasterEvent() *Event {
	if r.Master != nil {
		return ReadEvent(r.Master, r.Loc)
	}
	keys := r.overrideKeys()
	return ReadEvent(r.Overrides[keys[0]], r.Loc)
}

func (r *Resource) overrideKeys() []string {
	keys := make([]string, 0, len(r.Overrides))
	for k := range r.Overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Occurrence is one instance of an event series.
type Occurrence struct {
	Key       string // RecurrenceKey of the original start ("" for a non-recurring event)
	Start     time.Time
	End       time.Time
	AllDay    bool
	Event     *Event // the master's data, or the override's
	Override  bool
	Recurring bool
}

// RuleSet builds the rrule set of the master (nil when it does not repeat).
func (r *Resource) RuleSet(m *Event) (*rrule.Set, error) {
	if m.RRule == "" && len(m.RDates) == 0 {
		return nil, nil
	}
	set := &rrule.Set{}
	set.DTStart(m.Start)
	if m.RRule != "" {
		opt, err := rrule.StrToROptionInLocation(m.RRule, m.Zone)
		if err != nil {
			return nil, err
		}
		opt.Dtstart = m.Start
		if !opt.Until.IsZero() && m.AllDay {
			// an all-day UNTIL is a date; include that whole day
			opt.Until = time.Date(opt.Until.Year(), opt.Until.Month(), opt.Until.Day(), 23, 59, 59, 0, m.Zone)
		}
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, err
		}
		set.RRule(rule)
	} else {
		set.RDate(m.Start)
	}
	for _, d := range m.RDates {
		set.RDate(d)
	}
	return set, nil
}

const maxPerSeries = 3000

// Expand returns the occurrences overlapping [from, to).
func (r *Resource) Expand(from, to time.Time) []Occurrence {
	var out []Occurrence
	seen := map[string]bool{}
	if r.Master != nil {
		m := ReadEvent(r.Master, r.Loc)
		dur := m.End.Sub(m.Start)
		set, err := r.RuleSet(m)
		if set == nil || err != nil {
			if overlaps(m.Start, m.End, m.AllDay, from, to) {
				out = append(out, Occurrence{Start: m.Start, End: m.End, AllDay: m.AllDay, Event: m})
			}
			return out
		}
		ex := map[string]bool{}
		for _, t := range m.ExDates {
			ex[RecurrenceKey(t, m.AllDay)] = true
		}
		// occurrences that started before `from` may still be running
		after := from.Add(-dur - 24*time.Hour)
		n := 0
		iter := set.Iterator()
		for {
			t, ok := iter()
			if !ok || !t.Before(to.Add(24*time.Hour)) || n > maxPerSeries {
				break
			}
			if t.Before(after) {
				continue
			}
			n++
			t = t.In(m.Zone)
			key := RecurrenceKey(t, m.AllDay)
			if ex[key] {
				continue
			}
			seen[key] = true
			if o, ok := r.Overrides[key]; ok {
				oe := ReadEvent(o, m.Zone)
				if oe.Status == StatusCancelled {
					continue
				}
				if overlaps(oe.Start, oe.End, oe.AllDay, from, to) {
					out = append(out, Occurrence{Key: key, Start: oe.Start, End: oe.End, AllDay: oe.AllDay, Event: oe, Override: true, Recurring: true})
				}
				continue
			}
			end := t.Add(dur)
			if m.AllDay {
				end = t.AddDate(0, 0, int(dur.Round(24*time.Hour)/(24*time.Hour)))
			}
			if overlaps(t, end, m.AllDay, from, to) {
				out = append(out, Occurrence{Key: key, Start: t, End: end, AllDay: m.AllDay, Event: m, Recurring: true})
			}
		}
	}
	// overrides moved in from outside the scanned window, or without a master
	for _, key := range r.overrideKeys() {
		if seen[key] {
			continue
		}
		o := r.Overrides[key]
		oe := ReadEvent(o, r.zone())
		if oe.Status == StatusCancelled && r.Master != nil {
			continue
		}
		if overlaps(oe.Start, oe.End, oe.AllDay, from, to) {
			out = append(out, Occurrence{Key: key, Start: oe.Start, End: oe.End, AllDay: oe.AllDay, Event: oe, Override: true, Recurring: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Occurrence finds one instance by its key (the master when key is "").
func (r *Resource) Occurrence(key string) (*Occurrence, bool) {
	if key == "" {
		if r.Master == nil {
			return nil, false
		}
		m := ReadEvent(r.Master, r.Loc)
		return &Occurrence{Start: m.Start, End: m.End, AllDay: m.AllDay, Event: m}, true
	}
	if o, ok := r.Overrides[key]; ok {
		oe := ReadEvent(o, r.zone())
		return &Occurrence{Key: key, Start: oe.Start, End: oe.End, AllDay: oe.AllDay, Event: oe, Override: true, Recurring: true}, true
	}
	if r.Master == nil {
		return nil, false
	}
	m := ReadEvent(r.Master, r.Loc)
	t, allDay, ok := ParseRecurrenceKey(key, m.Zone)
	if !ok {
		return nil, false
	}
	if allDay != m.AllDay {
		return nil, false
	}
	t = t.In(m.Zone)
	dur := m.End.Sub(m.Start)
	end := t.Add(dur)
	if m.AllDay {
		end = t.AddDate(0, 0, int(dur.Round(24*time.Hour)/(24*time.Hour)))
	}
	return &Occurrence{Key: key, Start: t, End: end, AllDay: m.AllDay, Event: m, Recurring: true}, true
}

// Span returns the UTC window every occurrence falls in (end is far in the
// future for an endless series). Padded by a day for floating and all-day times.
func (r *Resource) Span() (time.Time, time.Time) {
	var start, end time.Time
	grow := func(s, e time.Time) {
		if start.IsZero() || s.Before(start) {
			start = s
		}
		if e.After(end) {
			end = e
		}
	}
	if r.Master != nil {
		m := ReadEvent(r.Master, r.Loc)
		grow(m.Start, m.End)
		if set, err := r.RuleSet(m); set != nil && err == nil {
			opt, _ := rrule.StrToROptionInLocation(m.RRule, m.Zone)
			if m.RRule != "" && opt != nil && opt.Count == 0 && opt.Until.IsZero() {
				grow(m.Start, Forever)
			} else {
				dur := m.End.Sub(m.Start)
				iter := set.Iterator()
				for i := 0; i < 20000; i++ {
					t, ok := iter()
					if !ok {
						break
					}
					grow(t, t.Add(dur))
				}
			}
		}
	}
	for _, o := range r.Overrides {
		oe := ReadEvent(o, r.zone())
		grow(oe.Start, oe.End)
	}
	return start.UTC().Add(-24 * time.Hour), end.UTC().Add(24 * time.Hour)
}

// Forever stands for "no end" in range columns.
var Forever = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

func overlaps(s, e time.Time, allDay bool, from, to time.Time) bool {
	if !e.After(s) {
		// zero-length events show on the moment they happen
		return !s.Before(from) && s.Before(to)
	}
	return s.Before(to) && e.After(from)
}

// DescribeRule gives a short human reading of an RRULE ("Every 2 weeks on Mon, Wed").
func DescribeRule(s string) string {
	if s == "" {
		return ""
	}
	opt, err := rrule.StrToROption(strings.TrimPrefix(s, "RRULE:"))
	if err != nil {
		return "Repeats"
	}
	unit := map[rrule.Frequency]string{rrule.DAILY: "day", rrule.WEEKLY: "week", rrule.MONTHLY: "month", rrule.YEARLY: "year", rrule.HOURLY: "hour", rrule.MINUTELY: "minute"}[opt.Freq]
	var b strings.Builder
	if opt.Interval > 1 {
		b.WriteString("Every " + itoa(opt.Interval) + " " + unit + "s")
	} else {
		switch opt.Freq {
		case rrule.DAILY:
			b.WriteString("Daily")
		case rrule.WEEKLY:
			b.WriteString("Weekly")
		case rrule.MONTHLY:
			b.WriteString("Monthly")
		case rrule.YEARLY:
			b.WriteString("Yearly")
		default:
			b.WriteString("Every " + unit)
		}
	}
	if len(opt.Byweekday) > 0 {
		names := map[int]string{0: "Mon", 1: "Tue", 2: "Wed", 3: "Thu", 4: "Fri", 5: "Sat", 6: "Sun"}
		var ds []string
		for _, w := range opt.Byweekday {
			n := names[w.Day()]
			if w.N() != 0 {
				n = ordinal(w.N()) + " " + n
			}
			ds = append(ds, n)
		}
		if len(ds) == 5 && opt.Freq == rrule.WEEKLY && strings.Join(ds, ",") == "Mon,Tue,Wed,Thu,Fri" {
			return strings.Replace(b.String(), "Weekly", "Every weekday", 1) + untilText(opt)
		}
		b.WriteString(" on " + strings.Join(ds, ", "))
	}
	if len(opt.Bymonthday) > 0 {
		var ds []string
		for _, d := range opt.Bymonthday {
			ds = append(ds, ordinal(d))
		}
		b.WriteString(" on the " + strings.Join(ds, ", "))
	}
	return b.String() + untilText(opt)
}

func untilText(opt *rrule.ROption) string {
	switch {
	case opt.Count > 0:
		if opt.Count == 1 {
			return ", once"
		}
		return ", " + itoa(opt.Count) + " times"
	case !opt.Until.IsZero():
		return ", until " + opt.Until.Format("Jan 2, 2006")
	}
	return ""
}

func ordinal(n int) string {
	if n == -1 {
		return "last"
	}
	if n < 0 {
		return itoa(-n) + ordSuffix(-n) + " to last"
	}
	return itoa(n) + ordSuffix(n)
}

func ordSuffix(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return "th"
	}
	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	}
	return "th"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// TruncateRule ends a rule just before `before` (for "this and following").
func TruncateRule(rule string, before time.Time, allDay bool) string {
	parts := strings.Split(strings.TrimPrefix(rule, "RRULE:"), ";")
	out := parts[:0]
	for _, p := range parts {
		up := strings.ToUpper(p)
		if strings.HasPrefix(up, "UNTIL=") || strings.HasPrefix(up, "COUNT=") || p == "" {
			continue
		}
		out = append(out, p)
	}
	if allDay {
		out = append(out, "UNTIL="+before.AddDate(0, 0, -1).Format("20060102"))
	} else {
		out = append(out, "UNTIL="+before.Add(-time.Second).UTC().Format("20060102T150405Z"))
	}
	return strings.Join(out, ";")
}

// RemainingRule is the rule for the new series of a "this and following"
// split: COUNT is reduced by the occurrences already past.
func RemainingRule(rule string, done int) string {
	parts := strings.Split(strings.TrimPrefix(rule, "RRULE:"), ";")
	for i, p := range parts {
		if strings.HasPrefix(strings.ToUpper(p), "COUNT=") {
			n := 0
			for _, ch := range p[6:] {
				n = n*10 + int(ch-'0')
			}
			n -= done
			if n < 1 {
				n = 1
			}
			parts[i] = "COUNT=" + itoa(n)
		}
	}
	return strings.Join(parts, ";")
}
