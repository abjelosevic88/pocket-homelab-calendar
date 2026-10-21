package ical

import (
	"fmt"
	"strings"
	"time"
)

// Dates in the API and the index are "floating local" strings: "2006-01-02"
// for a day, "2006-01-02T15:04:05" for a moment in the person's zone. The
// iCalendar side may carry DATE, floating DATE-TIME, DATE-TIME with TZID or
// UTC; everything is folded into the person's zone for the index.

const (
	DayLayout  = "2006-01-02"
	TimeLayout = "2006-01-02T15:04:05"
)

// IsDay reports whether a floating value is a whole day.
func IsDay(v string) bool { return len(v) == len(DayLayout) }

// ParseLocal parses a floating value in loc (zero time and false when empty or bad).
func ParseLocal(v string, loc *time.Location) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if IsDay(v) {
		t, err := time.ParseInLocation(DayLayout, v, loc)
		return t, err == nil
	}
	t, err := time.ParseInLocation(TimeLayout, v, loc)
	if err != nil {
		if t, err = time.ParseInLocation("2006-01-02T15:04", v, loc); err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// ValidLocal reports whether v is a well-formed floating value.
func ValidLocal(v string) bool {
	if v == "" {
		return true
	}
	_, ok := ParseLocal(v, time.UTC)
	return ok
}

// FormatLocal writes a floating value.
func FormatLocal(t time.Time, allDay bool) string {
	if allDay {
		return t.Format(DayLayout)
	}
	return t.Format(TimeLayout)
}

// Day returns the day part of a floating value.
func Day(v string) string {
	if len(v) >= len(DayLayout) {
		return v[:len(DayLayout)]
	}
	return v
}

// ReadDateTime reads a DATE / DATE-TIME property into a floating value in loc
// and reports whether it was a whole day. The TZID is resolved when known;
// an unknown TZID is read as floating.
func ReadDateTime(p *Property, loc *time.Location) (string, bool) {
	if p == nil || p.Value == "" {
		return "", false
	}
	v := strings.TrimSpace(p.Value)
	if strings.EqualFold(p.Param("VALUE"), "DATE") || len(v) == 8 {
		if t, err := time.Parse("20060102", v); err == nil {
			return t.Format(DayLayout), true
		}
		return "", false
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
			return "", false
		}
		return t.In(loc).Format(TimeLayout), false
	case p.Param("TZID") != "":
		z, err := time.LoadLocation(cleanTZID(p.Param("TZID")))
		if err != nil {
			z = loc
		}
		t, err := time.ParseInLocation(layout, v, z)
		if err != nil {
			return "", false
		}
		return t.In(loc).Format(TimeLayout), false
	default:
		t, err := time.Parse(layout, v)
		if err != nil {
			return "", false
		}
		return t.Format(TimeLayout), false
	}
}

// cleanTZID maps the odd forms clients send onto IANA names.
func cleanTZID(z string) string {
	z = strings.Trim(z, `"`)
	z = strings.TrimPrefix(z, "/freeassociation.sourceforge.net/")
	z = strings.TrimPrefix(z, "/mozilla.org/20070129_1/")
	if i := strings.LastIndex(z, "/"); i >= 0 && strings.Count(z, "/") > 1 {
		// "/citadel.org/20210210_1/Europe/Berlin" → keep the last two segments
		parts := strings.Split(z, "/")
		if len(parts) >= 2 {
			z = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	switch z {
	case "GMT", "UTC", "Z":
		return "UTC"
	}
	return z
}

// DateProperty writes a floating value as a DATE or floating DATE-TIME
// property. Floating times are what Apple Reminders writes too.
func DateProperty(name, v string) Property {
	if IsDay(v) {
		return Property{Name: name, Params: []Param{{Name: "VALUE", Values: []string{"DATE"}}}, Value: strings.ReplaceAll(v, "-", "")}
	}
	t, _ := ParseLocal(v, time.UTC)
	return Property{Name: name, Value: t.Format("20060102T150405")}
}

// ZonedProperty writes a floating value with a TZID (for events, where
// calendar apps want an absolute moment).
func ZonedProperty(name, v, tzid string) Property {
	if IsDay(v) {
		return DateProperty(name, v)
	}
	t, _ := ParseLocal(v, time.UTC)
	return Property{Name: name, Params: []Param{{Name: "TZID", Values: []string{tzid}}}, Value: t.Format("20060102T150405")}
}

// UTCProperty writes an absolute time as DATE-TIME in UTC.
func UTCProperty(name string, t time.Time) Property {
	return Property{Name: name, Value: t.UTC().Format("20060102T150405Z")}
}

// ReadUTC reads an absolute DATE-TIME (COMPLETED, CREATED, LAST-MODIFIED…).
func ReadUTC(p *Property, loc *time.Location) (time.Time, bool) {
	if p == nil || p.Value == "" {
		return time.Time{}, false
	}
	v := strings.TrimSpace(p.Value)
	if len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, loc)
		return t, err == nil
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse("20060102T150405Z", v)
		return t, err == nil
	}
	z := loc
	if tz := p.Param("TZID"); tz != "" {
		if l, err := time.LoadLocation(cleanTZID(tz)); err == nil {
			z = l
		}
	}
	t, err := time.ParseInLocation("20060102T150405", v, z)
	return t, err == nil
}

// Duration reads an ISO 8601 duration ("-PT15M", "P1DT2H", "PT0S") into a
// Go duration; weeks count as 7 days.
func Duration(s string) (time.Duration, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	} else if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	s = s[1:]
	var d time.Duration
	inTime := false
	num := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num = num*10 + int(r-'0')
		case r == 'T':
			inTime = true
		case r == 'W':
			d += time.Duration(num) * 7 * 24 * time.Hour
			num = 0
		case r == 'D':
			d += time.Duration(num) * 24 * time.Hour
			num = 0
		case r == 'H' && inTime:
			d += time.Duration(num) * time.Hour
			num = 0
		case r == 'M' && inTime:
			d += time.Duration(num) * time.Minute
			num = 0
		case r == 'S' && inTime:
			d += time.Duration(num) * time.Second
			num = 0
		default:
			return 0, fmt.Errorf("bad duration %q", s)
		}
	}
	if neg {
		d = -d
	}
	return d, nil
}

// FormatDuration writes a Go duration as ISO 8601 ("-PT15M", "PT0S", "P1D").
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	sec := d / time.Second
	var b strings.Builder
	b.WriteString(sign)
	b.WriteString("P")
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}
	if h > 0 || m > 0 || sec > 0 {
		b.WriteString("T")
		if h > 0 {
			fmt.Fprintf(&b, "%dH", h)
		}
		if m > 0 {
			fmt.Fprintf(&b, "%dM", m)
		}
		if sec > 0 {
			fmt.Fprintf(&b, "%dS", sec)
		}
	}
	return b.String()
}
