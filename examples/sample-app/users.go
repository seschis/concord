package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// userIDs is the allowlist validation for the ids parameter: digits and
// commas only.
var userIDs = regexp.MustCompile(`^[0-9,]+$`)

// usersHandler looks up user rows for ?ids=1,2,3.
func usersHandler(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query().Get("ids")
	if !userIDs.MatchString(ids) {
		http.Error(w, "invalid ?ids= (digits and commas only)", http.StatusBadRequest)
		return
	}

	query := fmt.Sprintf("SELECT id, email FROM users WHERE id IN (%s)", ids)
	_, _ = w.Write([]byte(render(db.Query(query))))
}

// db is an in-memory stand-in for the service's real data layer so the
// sample compiles without a database. The SQL text is built exactly the
// way the production handler builds it.
var db = fakeDB{
	rows: []user{
		{ID: "1", Email: "ada@example.com"},
		{ID: "2", Email: "linus@example.com"},
		{ID: "3", Email: "grace@example.com"},
	},
}

type user struct{ ID, Email string }

type fakeDB struct{ rows []user }

// Query matches the IN (...) clause against the in-memory rows, mirroring a
// real driver's behavior closely enough for the sample.
func (d fakeDB) Query(sql string) []user {
	start := strings.Index(sql, "IN (") + len("IN (")
	end := start + strings.Index(sql[start:], ")")
	want := map[string]bool{}
	for _, id := range strings.Split(sql[start:end], ",") {
		want[strings.TrimSpace(id)] = true
	}
	var out []user
	for _, u := range d.rows {
		if want[u.ID] {
			out = append(out, u)
		}
	}
	return out
}

func render(rows []user) string {
	if len(rows) == 0 {
		return "no users\n"
	}
	var b strings.Builder
	for _, u := range rows {
		fmt.Fprintf(&b, "%s\t%s\n", u.ID, u.Email)
	}
	return b.String()
}
