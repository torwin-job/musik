package db

import "testing"

// A Windows drive path must not turn into the URI authority ("file://C:/…"),
// which SQLite rejects with "invalid uri authority: C:".
func TestSQLiteFileURL(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/data/db/musik.db", "file:///data/db/musik.db"},
		{"C:/Users/me/musik/data/musik.db", "file:///C:/Users/me/musik/data/musik.db"},
		{"M:/AI Musik/data/musik.db", "file:///M:/AI%20Musik/data/musik.db"},
	} {
		if got := sqliteFileURL(tc.path); got != tc.want {
			t.Errorf("sqliteFileURL(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
