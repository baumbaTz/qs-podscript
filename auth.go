package main

import (
	"bufio"
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server mode (qs-podscript server): the same app for many people.
// Everybody may read (podcast list, transcripts, audio, manual). Logged-in
// editors may fix transcripts of the podcasts assigned to them. Admins may do
// everything, incl. users, podcasts, setup and transcribing.
// In the normal local mode none of this applies: you are the admin.

const (
	roleAdmin  = "admin"
	roleEditor = "editor"

	sessionCookie = "qsps_session"
	sessionTTL    = 30 * 24 * time.Hour
	pbkdfRounds   = 600000
)

type User struct {
	ID          int64
	Name        string
	Role        string
	Podcasts    map[int64]bool // editor: podcasts (feed ids) they may edit
	DisplayName string         // name shown publicly if ShowName (empty: login name)
	ShowName    bool           // false: shown as "Volunteer <id>"
}

// PublicName is how others (not admins) see this user in credits.
func (u *User) PublicName() string {
	if u == nil {
		return ""
	}
	if !u.ShowName {
		return fmt.Sprintf("Volunteer %d", u.ID)
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Name
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == roleAdmin }

var authSchema = []string{
	`CREATE TABLE users(
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
		pass_hash  TEXT NOT NULL,
		role       TEXT NOT NULL DEFAULT 'editor',
		created_at INTEGER NOT NULL)`,
	`CREATE TABLE user_podcasts(
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		PRIMARY KEY(user_id, feed_id))`,
	`CREATE TABLE sessions(
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires    INTEGER NOT NULL)`,
}

// ---------------------------------------------------------------- passwords

func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfRounds, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfRounds,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(stored, pw string) bool {
	p := strings.Split(stored, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	rounds, err := strconv.Atoi(p[1])
	if err != nil || rounds < 1 {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[2])
	want, err2 := base64.RawStdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, rounds, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func validPassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("the password needs at least 10 characters")
	}
	return nil
}

// ---------------------------------------------------------------- store

func (s *Store) AddUser(name, pw, role string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("the user needs a name")
	}
	if role != roleAdmin && role != roleEditor {
		return 0, errors.New("unknown role")
	}
	if err := validPassword(pw); err != nil {
		return 0, err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO users(name,pass_hash,role,created_at) VALUES(?,?,?,?)`, name, h, role, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, fmt.Errorf("a user called %q exists already", name)
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) SetPassword(userID int64, pw string) error {
	if err := validPassword(pw); err != nil {
		return err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE users SET pass_hash=? WHERE id=?`, h, userID)
	if err == nil {
		s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, userID) // log out everywhere
	}
	return err
}

func (s *Store) SetUserRole(userID int64, role string) error {
	if role != roleAdmin && role != roleEditor {
		return errors.New("unknown role")
	}
	_, err := s.db.Exec(`UPDATE users SET role=? WHERE id=?`, role, userID)
	return err
}

func (s *Store) SetUserPodcasts(userID int64, feeds []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM user_podcasts WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, f := range feeds {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_podcasts(user_id,feed_id) VALUES(?,?)`, userID, f); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetPublicName(userID int64, display string, show bool) error {
	display = strings.Join(strings.Fields(display), " ")
	if len(display) > 60 {
		return errors.New("the name is too long (60 characters at most)")
	}
	_, err := s.db.Exec(`UPDATE users SET display_name=?, show_name=? WHERE id=?`, display, show, userID)
	return err
}

func (s *Store) DeleteUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id=?`, userID)
	return err
}

func (s *Store) userByQuery(q string, args ...any) (*User, string, error) {
	u := &User{Podcasts: map[int64]bool{}}
	var hash string
	if err := s.db.QueryRow(`SELECT id,name,role,pass_hash,display_name,show_name FROM users WHERE `+q, args...).
		Scan(&u.ID, &u.Name, &u.Role, &hash, &u.DisplayName, &u.ShowName); err != nil {
		return nil, "", err
	}
	rows, err := s.db.Query(`SELECT feed_id FROM user_podcasts WHERE user_id=?`, u.ID)
	if err == nil {
		for rows.Next() {
			var f int64
			rows.Scan(&f)
			u.Podcasts[f] = true
		}
		rows.Close()
	}
	return u, hash, nil
}

func (s *Store) Users() ([]User, error) {
	rows, err := s.db.Query(`SELECT id FROM users ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	var out []User
	for _, id := range ids {
		if u, _, err := s.userByQuery(`id=?`, id); err == nil {
			out = append(out, *u)
		}
	}
	return out, nil
}

func (s *Store) CountAdmins() int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role='admin'`).Scan(&n)
	return n
}

// ---------------------------------------------------------------- sessions

func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (s *Store) NewSession(userID int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.db.Exec(`DELETE FROM sessions WHERE expires < ?`, time.Now().Unix())
	_, err := s.db.Exec(`INSERT INTO sessions(token_hash,user_id,expires) VALUES(?,?,?)`,
		tokenHash(tok), userID, time.Now().Add(sessionTTL).Unix())
	return tok, err
}

func (s *Store) SessionUser(tok string) *User {
	if tok == "" {
		return nil
	}
	u, _, err := s.userByQuery(`id=(SELECT user_id FROM sessions WHERE token_hash=? AND expires>?)`, tokenHash(tok), time.Now().Unix())
	if err != nil {
		return nil
	}
	return u
}

func (s *Store) EndSession(tok string) {
	s.db.Exec(`DELETE FROM sessions WHERE token_hash=?`, tokenHash(tok))
}

// ---------------------------------------------------------------- request handling

type ctxKey int

const userKey ctxKey = 1

func currentUser(r *http.Request) *User {
	u, _ := r.Context().Value(userKey).(*User)
	return u
}

// withUser attaches the logged-in user (server mode) to the request.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.serverMode {
			var u *User
			if tok := bearerToken(r); tok != "" {
				u = s.st.TokenUser(tok) // connected QS-PodScript (API, pass-through)
			} else if c, err := r.Cookie(sessionCookie); err == nil {
				u = s.st.SessionUser(c.Value)
			}
			if u != nil {
				r = r.WithContext(context.WithValue(r.Context(), userKey, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// access levels for routes
type access int

const (
	accessPublic access = iota // everybody
	accessUser                 // any logged-in user
	accessEdit                 // editor of the podcast the request is about, or admin
	accessAdmin                // admins only
)

// guard wraps a handler with an access level. Local mode: everything allowed.
func (s *Server) guard(level access, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.serverMode || level == accessPublic {
			h(w, r)
			return
		}
		u := currentUser(r)
		if u == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "please log in", http.StatusUnauthorized)
			return
		}
		ok := u.IsAdmin()
		switch level {
		case accessUser:
			ok = true
		case accessEdit:
			if !ok {
				if feed, found := s.feedOfRequest(r); found {
					ok = u.Podcasts[feed]
				}
			}
		}
		if !ok {
			http.Error(w, "You don't have the rights for this. Ask an admin.", http.StatusForbidden)
			return
		}
		if level == accessEdit && r.Method == http.MethodPost {
			// record successful edits: who changed what (redirect without error, or 2xx)
			rec := &statusRecorder{ResponseWriter: w}
			h(rec, r)
			if act := editAction(r.URL.Path); act != "" && rec.succeeded() {
				s.audit(r, act, editDetail(r))
			}
			return
		}
		h(w, r)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	if s.code == 0 {
		s.code = c
	}
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) succeeded() bool {
	if s.code >= 300 && s.code < 400 {
		return !strings.Contains(s.Header().Get("Location"), "err=")
	}
	return s.code >= 200 && s.code < 300
}

func editAction(path string) string {
	switch {
	case strings.HasSuffix(path, "/carried/delete"):
		return actUndoCarried
	case strings.Contains(path, "/corrections/") && strings.HasSuffix(path, "/delete"):
		return actUndo
	case strings.HasSuffix(path, "/corrections"):
		return actCorrection
	case strings.HasPrefix(path, "/episodes/") && strings.HasSuffix(path, "/people"):
		return actEpisodePeople
	case strings.HasSuffix(path, "/check"):
		return actCheck
	case strings.HasSuffix(path, "/identify"):
		return actIdentify
	case strings.HasSuffix(path, "/voicemerge"):
		return actVoiceMerge
	case strings.Contains(path, "/spelling"):
		return actSpelling
	case strings.Contains(path, "/roster"):
		return actRoster
	}
	return ""
}

func editDetail(r *http.Request) string {
	switch k := r.FormValue("kind"); k {
	case "mark":
		if m := r.FormValue("mark"); m != "" {
			return "marked as " + markName(m)
		}
		return "removed a mark"
	case "text":
		return "text"
	case "range", "merge":
		return "speaker"
	}
	if c := r.FormValue("correct"); c != "" {
		return c
	}
	return ""
}

// feedOfRequest finds the podcast a request is about: /feeds/{id}/…,
// /episodes/{id}/…, /versions/{vid}/….
func (s *Server) feedOfRequest(r *http.Request) (int64, bool) {
	if v := r.PathValue("vid"); v != "" {
		vid, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false
		}
		var feed int64
		err = s.st.db.QueryRow(`SELECT e.feed_id FROM versions v JOIN episodes e ON e.id=v.episode_id WHERE v.id=?`, vid).Scan(&feed)
		return feed, err == nil
	}
	id, ok := pathID(r)
	if !ok {
		return 0, false
	}
	if strings.HasPrefix(r.URL.Path, "/episodes/") {
		var feed int64
		err := s.st.db.QueryRow(`SELECT feed_id FROM episodes WHERE id=?`, id).Scan(&feed)
		return feed, err == nil
	}
	if strings.HasPrefix(r.URL.Path, "/feeds/") {
		return id, true
	}
	return 0, false
}

// canEdit: may the viewer change things of this podcast?
func (s *Server) canEdit(r *http.Request, feedID int64) bool {
	if !s.serverMode {
		return true
	}
	u := currentUser(r)
	return u.IsAdmin() || (u != nil && u.Podcasts[feedID])
}

func (s *Server) isAdmin(r *http.Request) bool {
	return !s.serverMode || currentUser(r).IsAdmin()
}

// ---------------------------------------------------------------- login

var loginFails = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

// trustedProxies: addresses whose X-Forwarded-For header is believed
// (loopback always; more with "server --trusted-proxy").
var trustedProxies []*net.IPNet

func setTrustedProxies(list string) error {
	trustedProxies = nil
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			return fmt.Errorf("--trusted-proxy: %q is not an address or network", p)
		}
		trustedProxies = append(trustedProxies, n)
	}
	return nil
}

func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// behind the reverse proxy everything comes from the proxy; believe its
	// header only when the request really comes from a trusted proxy. The
	// last entry is the one the proxy added itself (earlier ones can be
	// sent by the client).
	if isTrustedProxy(net.ParseIP(host)) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if f := strings.TrimSpace(parts[len(parts)-1]); f != "" {
			return f
		}
	}
	return host
}

// tooManyFails: more than 8 failed logins from one address in 15 minutes.
func tooManyFails(ip string) bool {
	loginFails.Lock()
	defer loginFails.Unlock()
	var keep []time.Time
	for _, t := range loginFails.m[ip] {
		if time.Since(t) < 15*time.Minute {
			keep = append(keep, t)
		}
	}
	loginFails.m[ip] = keep
	return len(keep) >= 8
}

func noteFail(ip string) {
	loginFails.Lock()
	loginFails.m[ip] = append(loginFails.m[ip], time.Now())
	loginFails.Unlock()
}

func safeNext(n string) string {
	if n == "" || !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.Contains(n, "\\") {
		return "/"
	}
	return n
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login", "Log in", "login", map[string]any{"Next": safeNext(r.URL.Query().Get("next"))})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	ip := clientIP(r)
	fail := func(msg string) {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(next)+"&err="+url.QueryEscape(msg), http.StatusSeeOther)
	}
	if tooManyFails(ip) {
		fail("Too many failed attempts. Try again in 15 minutes.")
		return
	}
	u, hash, err := s.st.userByQuery(`name=?`, strings.TrimSpace(r.FormValue("name")))
	if err != nil || !checkPassword(hash, r.FormValue("password")) {
		noteFail(ip)
		time.Sleep(700 * time.Millisecond)
		fail("Name or password is wrong.")
		return
	}
	tok, err := s.st.NewSession(u.ID)
	if err != nil {
		fail(err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), Expires: time.Now().Add(sessionTTL)})
	s.st.Audit(u.ID, actLogin, 0, 0, 0, ip)
	logf("Login: %s (%s)", u.Name, ip)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.st.EndSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---------------------------------------------------------------- users page (admins)

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, _ := s.st.Users()
	sort.SliceStable(users, func(i, j int) bool { return strings.ToLower(users[i].Name) < strings.ToLower(users[j].Name) })
	stats := map[int64]UserStats{}
	for _, u := range users {
		stats[u.ID] = s.st.UserStats(u.ID)
	}
	feeds, _ := s.st.Feeds() // already alphabetical
	names := func(rights map[int64]bool) []string {
		var out []string
		for _, f := range feeds {
			if rights[f.ID] {
				out = append(out, f.Title)
			}
		}
		return out
	}
	data := map[string]any{"Users": users, "Stats": stats, "Feeds": feeds}
	invites, _ := s.st.Invites()
	type invRow struct {
		Invite
		Names []string
	}
	rows := make([]invRow, len(invites))
	for i, iv := range invites {
		rows[i] = invRow{iv, names(iv.Podcasts)}
	}
	data["OpenInvites"] = rows
	if tok := r.URL.Query().Get("invited"); tok != "" {
		if iv, err := s.st.inviteByToken(tok); err == nil {
			data["Invited"] = struct {
				Expires time.Time
				QR      template.HTML
				Link    string
				Names   []string
			}{iv.Expires, inviteQR(origin(r) + "/join/" + tok), origin(r) + "/join/" + tok, names(iv.Podcasts)}
		}
	}
	s.render(w, r, "users", "Users", "users", data)
}

func (s *Server) handleUserAdd(w http.ResponseWriter, r *http.Request) {
	role := roleEditor
	if r.FormValue("admin") == "1" {
		role = roleAdmin
	}
	id, err := s.st.AddUser(r.FormValue("name"), r.FormValue("password"), role)
	if err != nil {
		back(w, r, "/users", "", err.Error())
		return
	}
	logf("User added: %s (%s) by %s", r.FormValue("name"), role, currentUser(r).Name)
	s.st.Audit(currentUser(r).ID, actUserAdmin, 0, 0, 0, "added "+r.FormValue("name"))
	back(w, r, fmt.Sprintf("/users/%d#rights", id), "User added. Tick the podcasts they may edit.", "")
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/users/%d", id)
	if err := r.ParseForm(); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	me := currentUser(r)
	role := roleEditor
	if r.FormValue("admin") == "1" {
		role = roleAdmin
	}
	if me != nil && me.ID == id && role != roleAdmin {
		back(w, r, to, "", "You can't take away your own admin rights.")
		return
	}
	var feeds []int64
	for _, v := range r.PostForm["podcast"] {
		if f, err := strconv.ParseInt(v, 10, 64); err == nil {
			feeds = append(feeds, f)
		}
	}
	if err := s.st.SetUserRole(id, role); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	if err := s.st.SetUserPodcasts(id, feeds); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	if pw := r.FormValue("password"); pw != "" {
		if err := s.st.SetPassword(id, pw); err != nil {
			back(w, r, to, "", err.Error())
			return
		}
	}
	s.st.Audit(me.ID, actUserAdmin, 0, 0, 0, fmt.Sprintf("changed user %d", id))
	back(w, r, to, "Saved.", "")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if me := currentUser(r); me != nil && me.ID == id {
		back(w, r, "/users", "", "You can't remove yourself.")
		return
	}
	if r.FormValue("confirm") != "1" {
		back(w, r, fmt.Sprintf("/users/%d", id), "", "Tick the box to confirm.")
		return
	}
	if err := s.st.DeleteUser(id); err != nil {
		back(w, r, "/users", "", err.Error())
		return
	}
	s.st.Audit(currentUser(r).ID, actUserAdmin, 0, 0, 0, fmt.Sprintf("removed user %d", id))
	back(w, r, "/users", "User removed. Their corrections stay.", "")
}

// ---------------------------------------------------------------- CLI: qs-podscript user …

func cmdUser(args []string) error {
	usage := fmt.Errorf("usage: qs-podscript user list | add <name> [--admin] | passwd <name> | admin <name> | delete <name>")
	if len(args) == 0 {
		return usage
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	switch args[0] {
	case "list":
		us, _ := st.Users()
		if len(us) == 0 {
			fmt.Println("No users yet. Add the first admin with: qs-podscript user add <name> --admin")
		}
		for _, u := range us {
			fmt.Printf("%-20s %-7s %d podcasts\n", u.Name, u.Role, len(u.Podcasts))
		}
		return nil
	case "add":
		if len(args) < 2 {
			return usage
		}
		role := roleEditor
		if len(args) > 2 && args[2] == "--admin" {
			role = roleAdmin
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if _, err := st.AddUser(args[1], pw, role); err != nil {
			return err
		}
		fmt.Printf("User %s added (%s).\n", args[1], role)
		return nil
	case "passwd", "admin", "delete":
		if len(args) < 2 {
			return usage
		}
		u, _, err := st.userByQuery(`name=?`, args[1])
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user called %q", args[1])
		} else if err != nil {
			return err
		}
		switch args[0] {
		case "passwd":
			pw, err := readPassword()
			if err != nil {
				return err
			}
			if err := st.SetPassword(u.ID, pw); err != nil {
				return err
			}
			fmt.Println("Password changed (all sessions of this user were logged out).")
		case "admin":
			if err := st.SetUserRole(u.ID, roleAdmin); err != nil {
				return err
			}
			fmt.Println(u.Name, "is now an admin.")
		case "delete":
			if err := st.DeleteUser(u.ID); err != nil {
				return err
			}
			fmt.Println(u.Name, "removed.")
		}
		return nil
	}
	return usage
}

// readPassword reads a password from stdin (typed twice when interactive).
// It is shown while typing - run it in a private terminal.
func readPassword() (string, error) {
	in := bufio.NewReader(os.Stdin)
	fi, _ := os.Stdin.Stat()
	interactive := fi != nil && fi.Mode()&os.ModeCharDevice != 0
	if interactive {
		fmt.Print("Password (at least 10 characters): ")
	}
	pw, err := in.ReadString('\n')
	if err != nil && pw == "" {
		return "", errors.New("no password given")
	}
	pw = strings.TrimRight(pw, "\r\n")
	if interactive {
		fmt.Print("Again: ")
		again, _ := in.ReadString('\n')
		if strings.TrimRight(again, "\r\n") != pw {
			return "", errors.New("the two passwords differ")
		}
	}
	return pw, validPassword(pw)
}
