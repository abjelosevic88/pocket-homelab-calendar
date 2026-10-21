// Package quick reads an event typed in plain words:
//
//	Dinner with Sani fri 19:30-21 at Kod Keke #family remind 1h
//	Dentist tomorrow 3pm for 45m
//	Standup every weekday 9:15 for 15m #work
//	Mum's birthday 12 may every year
package quick

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Result is what was understood.
type Result struct {
	Title      string    `json:"title"`
	Start      time.Time `json:"-"`
	End        time.Time `json:"-"`
	AllDay     bool      `json:"all_day"`
	HasDate    bool      `json:"has_date"`
	HasTime    bool      `json:"has_time"`
	RRule      string    `json:"rrule,omitempty"`
	Location   string    `json:"location,omitempty"`
	Calendar   string    `json:"calendar,omitempty"`
	Alarms     []string  `json:"alarms,omitempty"`
	With       []string  `json:"with,omitempty"` // names or emails after "with"
	Busy       *bool     `json:"busy,omitempty"`
	Private    bool      `json:"private,omitempty"`
	Recognized []string  `json:"recognized"`
}

var (
	weekdays = map[string]time.Weekday{"sun": 0, "sunday": 0, "mon": 1, "monday": 1, "tue": 2, "tues": 2, "tuesday": 2,
		"wed": 3, "wednesday": 3, "thu": 4, "thur": 4, "thurs": 4, "thursday": 4, "fri": 5, "friday": 5, "sat": 6, "saturday": 6}
	months = map[string]time.Month{"jan": 1, "january": 1, "feb": 2, "february": 2, "mar": 3, "march": 3, "apr": 4, "april": 4,
		"may": 5, "jun": 6, "june": 6, "jul": 7, "july": 7, "aug": 8, "august": 8, "sep": 9, "sept": 9, "september": 9,
		"oct": 10, "october": 10, "nov": 11, "november": 11, "dec": 12, "december": 12}
	byday = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

	reTimeRange = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm|h)?\s*[-–]\s*(\d{1,2})(?::(\d{2}))?\s*(am|pm|h)?$`)
	reTime      = regexp.MustCompile(`^(\d{1,2})(?::|\.)?(\d{2})?\s*(am|pm|h)?$`)
	reISO       = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)
	reDMY       = regexp.MustCompile(`^(\d{1,2})[./](\d{1,2})(?:[./](\d{2,4}))?\.?$`)
	reDur       = regexp.MustCompile(`^(\d+(?:[.,]\d+)?)\s*(m|min|mins|minutes?|h|hr|hrs|hours?|d|days?)$`)
	reEmail     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[a-z]{2,}$`)
)

// Parse reads text relative to now in loc.
func Parse(text string, now time.Time, loc *time.Location, weekStart time.Weekday) Result {
	now = now.In(loc)
	r := Result{}
	words := strings.Fields(text)
	used := make([]bool, len(words))
	low := func(i int) string { return strings.ToLower(strings.Trim(words[i], ",;")) }
	mark := func(i, n int, what string) {
		for k := i; k < i+n && k < len(words); k++ {
			used[k] = true
		}
		r.Recognized = append(r.Recognized, what)
	}
	var date time.Time
	var startH, startM, endH, endM = -1, 0, -1, 0
	var dur time.Duration

	for i := 0; i < len(words); i++ {
		if used[i] {
			continue
		}
		w := low(i)
		next := ""
		if i+1 < len(words) {
			next = low(i + 1)
		}
		switch {
		case strings.HasPrefix(w, "#") && len(w) > 1:
			r.Calendar = strings.TrimPrefix(words[i], "#")
			mark(i, 1, "calendar")
		case w == "today":
			date = day(now)
			mark(i, 1, "date")
		case w == "tonight":
			date = day(now)
			if startH < 0 {
				startH = 19
			}
			mark(i, 1, "date")
		case w == "tomorrow" || w == "tmrw" || w == "tmr":
			date = day(now).AddDate(0, 0, 1)
			mark(i, 1, "date")
		case w == "noon":
			startH, startM = 12, 0
			mark(i, 1, "time")
		case w == "midnight":
			startH, startM = 0, 0
			mark(i, 1, "time")
		case w == "private":
			r.Private = true
			mark(i, 1, "private")
		case w == "free" && (i == len(words)-1 || used[i+1]):
			f := false
			r.Busy = &f
			mark(i, 1, "free")
		case (w == "next" || w == "this" || w == "on") && isWeekday(next):
			date = nextWeekday(now, weekdays[next], w == "next")
			mark(i, 2, "date")
			i++
		case isWeekday(w) && !(i > 0 && low(i-1) == "every"):
			date = nextWeekday(now, weekdays[w], false)
			mark(i, 1, "date")
		case w == "in" && i+2 < len(words) && isNumber(next) && strings.HasPrefix(low(i+2), "day"):
			n, _ := strconv.Atoi(next)
			date = day(now).AddDate(0, 0, n)
			mark(i, 3, "date")
			i += 2
		case w == "in" && i+2 < len(words) && isNumber(next) && strings.HasPrefix(low(i+2), "week"):
			n, _ := strconv.Atoi(next)
			date = day(now).AddDate(0, 0, 7*n)
			mark(i, 3, "date")
			i += 2
		case w == "next" && next == "week":
			date = nextWeekday(now, weekStart, true)
			mark(i, 2, "date")
			i++
		case reISO.MatchString(w):
			m := reISO.FindStringSubmatch(w)
			y, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			d, _ := strconv.Atoi(m[3])
			date = time.Date(y, time.Month(mo), d, 0, 0, 0, 0, loc)
			mark(i, 1, "date")
		case reDMY.MatchString(w) && !strings.Contains(w, ":"):
			m := reDMY.FindStringSubmatch(w)
			d, _ := strconv.Atoi(m[1])
			mo, _ := strconv.Atoi(m[2])
			if mo < 1 || mo > 12 || d < 1 || d > 31 {
				continue
			}
			y := now.Year()
			if m[3] != "" {
				y, _ = strconv.Atoi(m[3])
				if y < 100 {
					y += 2000
				}
			}
			date = time.Date(y, time.Month(mo), d, 0, 0, 0, 0, loc)
			if m[3] == "" && date.Before(day(now)) {
				date = date.AddDate(1, 0, 0)
			}
			mark(i, 1, "date")
		case isMonth(w) && isNumber(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(next, "st"), "nd"), "rd"), "th")):
			d, _ := strconv.Atoi(trimOrd(next))
			date = monthDay(now, months[w], d, loc)
			mark(i, 2, "date")
			i++
		case isNumber(trimOrd(w)) && isMonth(next):
			d, _ := strconv.Atoi(trimOrd(w))
			date = monthDay(now, months[next], d, loc)
			mark(i, 2, "date")
			i++
		case w == "every" || w == "daily" || w == "weekly" || w == "monthly" || w == "yearly" || w == "annually":
			n := r.parseRepeat(words, used, i)
			if n > 0 {
				mark(i, n, "repeat")
				i += n - 1
			}
		case w == "for" && i+1 < len(words):
			if d, n := parseDur(words[i+1:]); d > 0 {
				dur = d
				mark(i, 1+n, "duration")
				i += n
			}
		case (w == "remind" || w == "reminder" || w == "alert") && i+1 < len(words):
			k := i + 1
			if low(k) == "me" && k+1 < len(words) {
				k++
			}
			if d, n := parseDur(words[k:]); d > 0 {
				end := k + n
				if end < len(words) && low(end) == "before" {
					end++
				}
				r.Alarms = append(r.Alarms, isoDur(-d))
				mark(i, end-i, "reminder")
				i = end - 1
			}
		case w == "with" && i+1 < len(words):
			k := i + 1
			for k < len(words) && !used[k] {
				name := strings.Trim(words[k], ",;")
				lw := strings.ToLower(name)
				if lw == "and" || lw == "&" {
					k++
					continue
				}
				if reEmail.MatchString(lw) || (len(name) > 0 && strings.ToUpper(name[:1]) == name[:1] && !isWeekday(lw) && !isMonth(lw) && lw != "at") {
					r.With = append(r.With, name)
					k++
					if !strings.HasSuffix(words[k-1], ",") && (k >= len(words) || (strings.ToLower(words[k]) != "and" && strings.ToLower(words[k]) != "&")) {
						break
					}
					continue
				}
				break
			}
			if len(r.With) > 0 {
				// keep "Lunch with Sani" readable: the names stay in the title
				r.Recognized = append(r.Recognized, "with")
				i = k - 1
			}
		case (w == "at" || w == "@") && i+1 < len(words):
			if h, m, ok := parseClock(strings.Join(words[i+1:min(i+3, len(words))], " ")); ok && !isLocationWord(words[i+1]) {
				startH, startM = h, m
				n := 1
				if i+2 < len(words) && (low(i+2) == "am" || low(i+2) == "pm") {
					n = 2
				}
				mark(i, 1+n, "time")
				i += n
				continue
			}
			// a place: everything up to the next recognised keyword
			k := i + 1
			var place []string
			for k < len(words) && !used[k] && !isKeyword(low(k)) {
				place = append(place, words[k])
				k++
			}
			if len(place) > 0 {
				r.Location = strings.Trim(strings.Join(place, " "), ",;")
				mark(i, k-i, "location")
				i = k - 1
			}
		case w == "from" && i+1 < len(words):
			if h, m, ok := parseClock(low(i + 1)); ok {
				startH, startM = h, m
				n := 2
				if i+3 < len(words) && (low(i+2) == "to" || low(i+2) == "until" || low(i+2) == "till") {
					if eh, em, ok := parseClock(low(i + 3)); ok {
						endH, endM = eh, em
						n = 4
					}
				}
				mark(i, n, "time")
				i += n - 1
			}
		default:
			if m := reTimeRange.FindStringSubmatch(w); m != nil && (m[3] != "" || m[6] != "" || strings.Contains(w, ":") || looksLikeHour(m[1])) {
				sh, _ := strconv.Atoi(m[1])
				sm, _ := strconv.Atoi(m[2])
				eh, _ := strconv.Atoi(m[4])
				em, _ := strconv.Atoi(m[5])
				ap := m[6]
				if m[3] != "" {
					sh = ampm(sh, m[3])
				} else if ap == "pm" && sh < 12 && sh+12 <= ampm(eh, ap) {
					sh += 12
				}
				eh = ampm(eh, ap)
				if sh < 24 && eh <= 24 && sm < 60 && em < 60 {
					startH, startM, endH, endM = sh, sm, eh, em
					mark(i, 1, "time")
				}
			} else if h, m, ok := parseClock(w); ok && (strings.Contains(w, ":") || strings.HasSuffix(w, "am") || strings.HasSuffix(w, "pm") || strings.HasSuffix(w, "h")) {
				startH, startM = h, m
				mark(i, 1, "time")
			} else if (w == "am" || w == "pm") && i > 0 && used[i-1] {
				mark(i, 1, "time")
			}
		}
	}

	var title []string
	for i, w := range words {
		if !used[i] {
			title = append(title, w)
		}
	}
	r.Title = strings.TrimSpace(strings.Trim(strings.Join(title, " "), ",;-–"))
	if r.Title == "" {
		r.Title = "New event"
	}

	r.HasDate = !date.IsZero()
	r.HasTime = startH >= 0
	if date.IsZero() {
		date = day(now)
		if r.HasTime && time.Date(now.Year(), now.Month(), now.Day(), startH, startM, 0, 0, loc).Before(now) && r.RRule == "" {
			date = date.AddDate(0, 0, 1) // "3pm" said at 5pm means tomorrow
		}
	}
	if r.RRule != "" && !r.HasDate {
		// a weekly rule on given days starts on the first of them
		if days := ruleDays(r.RRule); len(days) > 0 {
			for d := 0; d < 7; d++ {
				t := day(now).AddDate(0, 0, d)
				if contains(days, byday[t.Weekday()]) {
					date = t
					break
				}
			}
		}
	}
	if !r.HasTime {
		r.AllDay = true
		r.Start = date
		r.End = date.AddDate(0, 0, 1)
		if dur >= 24*time.Hour {
			r.End = date.AddDate(0, 0, int(dur/(24*time.Hour)))
		}
		return r
	}
	r.Start = time.Date(date.Year(), date.Month(), date.Day(), startH, startM, 0, 0, loc)
	switch {
	case endH >= 0:
		r.End = time.Date(date.Year(), date.Month(), date.Day(), endH, endM, 0, 0, loc)
		if !r.End.After(r.Start) {
			r.End = r.End.AddDate(0, 0, 1)
		}
	case dur > 0:
		r.End = r.Start.Add(dur)
	}
	return r
}

func (r *Result) parseRepeat(words []string, used []bool, i int) int {
	w := strings.ToLower(words[i])
	simple := map[string]string{"daily": "FREQ=DAILY", "weekly": "FREQ=WEEKLY", "monthly": "FREQ=MONTHLY", "yearly": "FREQ=YEARLY", "annually": "FREQ=YEARLY"}
	if f, ok := simple[w]; ok {
		r.RRule = f
		return 1
	}
	if i+1 >= len(words) {
		return 0
	}
	n := 1
	k := i + 1
	interval := 1
	if v, err := strconv.Atoi(strings.ToLower(words[k])); err == nil && k+1 < len(words) {
		interval = v
		k++
	} else if strings.ToLower(words[k]) == "other" && k+1 < len(words) {
		interval = 2
		k++
	}
	unit := strings.ToLower(strings.Trim(words[k], ",;"))
	set := func(freq string) {
		r.RRule = "FREQ=" + freq
		if interval > 1 {
			r.RRule += ";INTERVAL=" + strconv.Itoa(interval)
		}
	}
	switch {
	case strings.HasPrefix(unit, "day"):
		set("DAILY")
	case strings.HasPrefix(unit, "weekday"):
		r.RRule = "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"
	case strings.HasPrefix(unit, "weekend"):
		r.RRule = "FREQ=WEEKLY;BYDAY=SA,SU"
	case strings.HasPrefix(unit, "week"):
		set("WEEKLY")
	case strings.HasPrefix(unit, "month"):
		set("MONTHLY")
	case strings.HasPrefix(unit, "year"):
		set("YEARLY")
	case isWeekday(unit):
		var days []string
		for k < len(words) {
			u := strings.ToLower(strings.Trim(words[k], ",;"))
			if isWeekday(u) {
				days = append(days, byday[weekdays[u]])
				k++
				continue
			}
			if u == "and" || u == "&" {
				k++
				continue
			}
			break
		}
		set("WEEKLY")
		r.RRule += ";BYDAY=" + strings.Join(days, ",")
		return k - i
	default:
		return 0
	}
	n = k - i + 1
	// "every week until dec 1" / "every day 5 times"
	return n
}

func ruleDays(rule string) []string {
	for _, p := range strings.Split(rule, ";") {
		if strings.HasPrefix(p, "BYDAY=") {
			return strings.Split(p[6:], ",")
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func day(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func nextWeekday(now time.Time, wd time.Weekday, skipThisWeek bool) time.Time {
	d := (int(wd) - int(now.Weekday()) + 7) % 7
	if d == 0 && skipThisWeek {
		d = 7
	}
	if skipThisWeek && d < 7 && d > 0 {
		// "next friday" on a monday is this friday in most people's speech
	}
	return day(now).AddDate(0, 0, d)
}

func monthDay(now time.Time, m time.Month, d int, loc *time.Location) time.Time {
	t := time.Date(now.Year(), m, d, 0, 0, 0, 0, loc)
	if t.Before(day(now)) {
		t = t.AddDate(1, 0, 0)
	}
	return t
}

func isWeekday(w string) bool { _, ok := weekdays[strings.Trim(w, ",;")]; return ok }
func isMonth(w string) bool   { _, ok := months[w]; return ok }
func isNumber(w string) bool {
	_, err := strconv.Atoi(w)
	return err == nil && w != ""
}
func trimOrd(w string) string {
	for _, s := range []string{"st", "nd", "rd", "th", "."} {
		w = strings.TrimSuffix(w, s)
	}
	return w
}

func looksLikeHour(h string) bool {
	n, err := strconv.Atoi(h)
	return err == nil && n <= 24
}

func ampm(h int, ap string) int {
	switch ap {
	case "pm":
		if h < 12 {
			return h + 12
		}
	case "am":
		if h == 12 {
			return 0
		}
	}
	return h
}

// parseClock reads "15:30", "3pm", "3:30 pm", "9h", "9".
func parseClock(s string) (int, int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " am", "am")
	s = strings.ReplaceAll(s, " pm", "pm")
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	m := reTime.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	if m[3] == "am" || m[3] == "pm" {
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		h = ampm(h, m[3])
	}
	if h > 23 || mi > 59 {
		return 0, 0, false
	}
	return h, mi, true
}

func parseDur(words []string) (time.Duration, int) {
	if len(words) == 0 {
		return 0, 0
	}
	w := strings.ToLower(strings.Trim(words[0], ",;"))
	n := 1
	if len(words) > 1 && isNumberF(w) {
		w += strings.ToLower(strings.Trim(words[1], ",;"))
		n = 2
	}
	m := reDur.FindStringSubmatch(w)
	if m == nil {
		// "1h30" / "1h30m"
		if mm := regexp.MustCompile(`^(\d+)h(\d+)m?$`).FindStringSubmatch(w); mm != nil {
			h, _ := strconv.Atoi(mm[1])
			mi, _ := strconv.Atoi(mm[2])
			return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute, n
		}
		return 0, 0
	}
	v, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
	switch m[2][0] {
	case 'm':
		return time.Duration(v * float64(time.Minute)), n
	case 'h':
		return time.Duration(v * float64(time.Hour)), n
	case 'd':
		return time.Duration(v * 24 * float64(time.Hour)), n
	}
	return 0, 0
}

func isNumberF(w string) bool {
	_, err := strconv.ParseFloat(strings.ReplaceAll(w, ",", "."), 64)
	return err == nil
}

func isoDur(d time.Duration) string {
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%sP%dD", sign, d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%sPT%dH", sign, d/time.Hour)
	default:
		return fmt.Sprintf("%sPT%dM", sign, d/time.Minute)
	}
}

var keywords = map[string]bool{"today": true, "tomorrow": true, "tonight": true, "every": true, "for": true, "remind": true,
	"with": true, "from": true, "on": true, "next": true, "daily": true, "weekly": true, "monthly": true, "yearly": true, "private": true}

func isKeyword(w string) bool {
	if keywords[w] || isWeekday(w) || strings.HasPrefix(w, "#") || reISO.MatchString(w) || reDMY.MatchString(w) {
		return true
	}
	if _, _, ok := parseClock(w); ok && (strings.Contains(w, ":") || strings.HasSuffix(w, "am") || strings.HasSuffix(w, "pm")) {
		return true
	}
	return reTimeRange.MatchString(w) && strings.ContainsAny(w, ":-")
}

func isLocationWord(w string) bool {
	// "at 5" is a time, "at 5th avenue" is not
	lw := strings.ToLower(w)
	return strings.HasSuffix(lw, "th") || strings.HasSuffix(lw, "st") || strings.HasSuffix(lw, "nd")
}
