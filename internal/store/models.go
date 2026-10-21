package store

// User is an account. Data is strictly per person; other people see only
// what is shared with them.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	Username     string `json:"username"` // CalDAV principal name
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	FeedToken    string `json:"-"`
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
// It doubles as the app password CalDAV clients sign in with.
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Calendar is one collection of events.
type Calendar struct {
	ID            int64    `json:"id"`
	UserID        int64    `json:"owner_id"`
	UID           string   `json:"uid"`
	Name          string   `json:"name"`
	Color         string   `json:"color"`
	Description   string   `json:"description"`
	Timezone      string   `json:"timezone"`
	Kind          string   `json:"kind"` // local | caldav | google | ics | nextcloud
	AccountID     *int64   `json:"account_id"`
	RemoteHref    string   `json:"remote_href,omitempty"`
	RemoteCTag    string   `json:"-"`
	RemoteToken   string   `json:"-"`
	ReadOnly      bool     `json:"read_only"`
	IsDefault     bool     `json:"is_default"`
	DefaultAlarms []string `json:"default_alarms"`
	FeedToken     string   `json:"-"`
	Public        bool     `json:"public"`
	SortOrder     float64  `json:"sort_order"`
	Seq           int64    `json:"-"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`

	// Filled per viewer.
	Role    string  `json:"role,omitempty"` // owner | manage | write | read | freebusy
	Visible bool    `json:"visible"`
	Notify  bool    `json:"notify"`
	MyColor string  `json:"my_color,omitempty"`
	Owner   *Person `json:"owner,omitempty"`
	Shares  []Share `json:"shares,omitempty"`
	Events  int     `json:"events,omitempty"`
}

// Person is the public face of an account (for sharing and attendees).
type Person struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Share grants someone (or everyone on the box) access to a calendar.
type Share struct {
	ID         int64   `json:"id"`
	CalendarID int64   `json:"calendar_id"`
	UserID     *int64  `json:"user_id"`
	Email      string  `json:"email,omitempty"`
	Everyone   bool    `json:"everyone"`
	Role       string  `json:"role"`
	Person     *Person `json:"person,omitempty"`
	Pending    bool    `json:"pending"`
	CreatedAt  string  `json:"created_at"`
}

// Account is an external calendar account.
type Account struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"-"`
	Kind        string `json:"kind"` // caldav | google | ics | nextcloud
	Name        string `json:"name"`
	URL         string `json:"url"`
	Username    string `json:"username"`
	Secret      string `json:"-"` // sealed
	OAuth       string `json:"-"` // sealed JSON
	Via         string `json:"via"`
	SyncMinutes int    `json:"sync_minutes"`
	Enabled     bool   `json:"enabled"`
	LastSyncAt  string `json:"last_sync_at"`
	LastError   string `json:"last_error"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	HasSecret   bool   `json:"has_secret"`
	Calendars   int    `json:"calendars"`
}

// EventRow is the stored calendar object with its index columns.
type EventRow struct {
	ID         int64  `json:"id"`
	CalendarID int64  `json:"calendar_id"`
	UID        string `json:"uid"`
	Href       string `json:"href"`
	ETag       string `json:"etag"`
	ICS        string `json:"-"`
	Summary    string `json:"summary"`
	Location   string `json:"location"`
	AllDay     bool   `json:"all_day"`
	DTStart    string `json:"dtstart"`
	DTEnd      string `json:"dtend"`
	TZID       string `json:"tzid"`
	RRule      string `json:"rrule"`
	Recurring  bool   `json:"recurring"`
	RangeStart string `json:"-"`
	RangeEnd   string `json:"-"`
	Status     string `json:"status"`
	Transp     string `json:"transp"`
	Class      string `json:"class"`
	Color      string `json:"color"`
	Organizer  string `json:"organizer"`
	Source     string `json:"source"`
	CreatedBy  int64  `json:"created_by"`
	UpdatedBy  int64  `json:"updated_by"`
	Seq        int64  `json:"-"`
	RemoteHref string `json:"-"`
	RemoteETag string `json:"-"`
	Dirty      bool   `json:"-"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// DeletedEvent is a restorable deleted event.
type DeletedEvent struct {
	ID         int64  `json:"id"`
	CalendarID int64  `json:"calendar_id"`
	UID        string `json:"uid"`
	Summary    string `json:"summary"`
	DTStart    string `json:"dtstart"`
	ICS        string `json:"-"`
	DeletedAt  string `json:"deleted_at"`
}

// Activity is one entry of a person's change log.
type Activity struct {
	ID         int64  `json:"id"`
	CalendarID *int64 `json:"calendar_id"`
	EventID    *int64 `json:"event_id"`
	Action     string `json:"action"`
	Summary    string `json:"summary"`
	Detail     string `json:"detail"`
	CreatedAt  string `json:"created_at"`
}
