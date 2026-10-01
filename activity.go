package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Server mode: who did what. Every change by a logged-in user is written to
// user_activity; versions remember whose computer transcribed them and
// corrections who made them. Admins see it per user (Users page), everybody
// sees on an episode who transcribed it and who cleaned it up.

var activitySchema = []string{
	`ALTER TABLE corrections ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE versions ADD COLUMN transcribed_by INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE versions ADD COLUMN transcribed_on TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE user_activity(
		id         INTEGER PRIMARY KEY,
		at         INTEGER NOT NULL,
		user_id    INTEGER NOT NULL,
		action     TEXT NOT NULL,
		feed_id    INTEGER NOT NULL DEFAULT 0,
		episode_id INTEGER NOT NULL DEFAULT 0,
		version_id INTEGER NOT NULL DEFAULT 0,
		detail     TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX idx_activity_user ON user_activity(user_id, at)`,
	`CREATE INDEX idx_activity_episode ON user_activity(episode_id)`,
}

// action names (shown via actionText)
const (
	actLogin         = "login"
	actConnect       = "connect"
	actTranscribed   = "transcribed"
	actJobFailed     = "job_failed"
	actJobRejected   = "job_rejected"
	actCorrection    = "correction"
	actUndo          = "undo"
	actUndoCarried   = "undo_carried"
	actIdentify      = "identify"
	actVoiceMerge    = "voice_merge"
	actSpelling      = "spelling"
	actRoster        = "roster"
	actPassword      = "password"
	actTokenRevoked  = "key_revoked"
	actUserAdmin     = "user_admin"
	actCheck         = "check"
	actEpisodePeople = "episode_people"
	actDiarized      = "diarized"
)

func actionText(a string) string {
	switch a {
	case actLogin:
		return "logged in"
	case actCheck:
		return "checked a passage"
	case actDiarized:
		return "redid the speaker detection of"
	case actEpisodePeople:
		return "set who is in an episode"
	case actConnect:
		return "connected a computer"
	case actTranscribed:
		return "transcribed"
	case actJobFailed:
		return "could not transcribe"
	case actJobRejected:
		return "result rejected"
	case actCorrection:
		return "corrected"
	case actUndo:
		return "undid a correction"
	case actUndoCarried:
		return "undid carried-over passages"
	case actIdentify:
		return "ran speaker identification"
	case actVoiceMerge:
		return "merged similar voices"
	case actSpelling:
		return "changed spelling fixes"
	case actRoster:
		return "changed people of a podcast"
	case actPassword:
		return "changed password"
	case actTokenRevoked:
		return "removed a computer's key"
	case actUserAdmin:
		return "managed users"
	}
	return a
}

func (s *Store) Audit(userID int64, action string, feedID, episodeID, versionID int64, detail string) {
	if userID == 0 {
		return
	}
	s.db.Exec(`INSERT INTO user_activity(at,user_id,action,feed_id,episode_id,version_id,detail) VALUES(?,?,?,?,?,?,?)`,
		time.Now().Unix(), userID, action, feedID, episodeID, versionID, detail)
}

// audit records a web action of the logged-in user (server mode only). The
// podcast / episode are derived from the request path like for rights.
func (s *Server) audit(r *http.Request, action, detail string) {
	if !s.serverMode {
		return
	}
	u := currentUser(r)
	if u == nil {
		return
	}
	var feed, ep, ver int64
	if v := r.PathValue("vid"); v != "" {
		ver, _ = strconv.ParseInt(v, 10, 64)
		s.st.db.QueryRow(`SELECT e.id, e.feed_id FROM versions v JOIN episodes e ON e.id=v.episode_id WHERE v.id=?`, ver).Scan(&ep, &feed)
	} else if f, ok := s.feedOfRequest(r); ok {
		feed = f
		if strings.HasPrefix(r.URL.Path, "/episodes/") {
			ep, _ = pathID(r)
		}
	}
	s.st.Audit(u.ID, action, feed, ep, ver, detail)
}

func (s *Server) userID(r *http.Request) int64 {
	if u := currentUser(r); s.serverMode && u != nil {
		return u.ID
	}
	return 0
}

// ---------------------------------------------------------------- statistics

type UserStats struct {
	Transcribed    int   // episodes (versions) transcribed on their computers
	TranscribedSec int64 // audio length of those
	CleanedEps     int   // episodes with corrections by them
	Corrections    int
	Checks         int // passages checked in "Look Who's Talking"
	LastActive     int64
}

func (s *Store) UserStats(userID int64) UserStats {
	var st UserStats
	var secs float64
	s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(audio_seconds),0) FROM versions WHERE transcribed_by=? AND status='done'`, userID).Scan(&st.Transcribed, &secs)
	st.TranscribedSec = int64(secs)
	s.db.QueryRow(`SELECT COUNT(DISTINCT v.episode_id), COUNT(*) FROM corrections c JOIN versions v ON v.id=c.version_id
		WHERE c.user_id=?`, userID).Scan(&st.CleanedEps, &st.Corrections)
	s.db.QueryRow(`SELECT COALESCE(MAX(at),0) FROM user_activity WHERE user_id=?`, userID).Scan(&st.LastActive)
	st.Checks = s.checksBy(userID)
	return st
}

type userEpisode struct {
	EpisodeID int64
	Title     string
	Podcast   string
	Count     int   // corrections (cleanup list)
	At        int64 // when
	Device    string
}

func (s *Store) episodesTranscribedBy(userID int64, limit int) []userEpisode {
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, v.created_at, v.transcribed_on
		FROM versions v JOIN episodes e ON e.id=v.episode_id JOIN feeds f ON f.id=e.feed_id
		WHERE v.transcribed_by=? AND v.status='done' ORDER BY v.created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []userEpisode
	for rows.Next() {
		var u userEpisode
		rows.Scan(&u.EpisodeID, &u.Title, &u.Podcast, &u.At, &u.Device)
		out = append(out, u)
	}
	return out
}

func (s *Store) episodesCleanedBy(userID int64, limit int) []userEpisode {
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, COUNT(*), MAX(c.created_at)
		FROM corrections c JOIN versions v ON v.id=c.version_id JOIN episodes e ON e.id=v.episode_id JOIN feeds f ON f.id=e.feed_id
		WHERE c.user_id=? GROUP BY e.id ORDER BY MAX(c.created_at) DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []userEpisode
	for rows.Next() {
		var u userEpisode
		rows.Scan(&u.EpisodeID, &u.Title, &u.Podcast, &u.Count, &u.At)
		out = append(out, u)
	}
	return out
}

type activityRow struct {
	At        int64
	User      string
	Action    string
	EpisodeID int64
	Episode   string
	Detail    string
}

func (s *Store) Activity(userID int64, limit int) []activityRow {
	q := `SELECT a.at, COALESCE(u.name,'(removed user)'), a.action, a.episode_id, COALESCE(e.title,''), a.detail
		FROM user_activity a LEFT JOIN users u ON u.id=a.user_id LEFT JOIN episodes e ON e.id=a.episode_id`
	var args []any
	if userID > 0 {
		q += ` WHERE a.user_id=?`
		args = append(args, userID)
	}
	q += ` ORDER BY a.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []activityRow
	for rows.Next() {
		var a activityRow
		rows.Scan(&a.At, &a.User, &a.Action, &a.EpisodeID, &a.Episode, &a.Detail)
		a.Action = actionText(a.Action)
		out = append(out, a)
	}
	return out
}

// episodeCredits: who cleaned up an episode. Admins see login names, everybody
// else the public name (anonymous "Volunteer <n>" unless the user chose to show
// a name).
type credit struct {
	Name  string
	Count int
}

func (s *Store) episodeCredits(episodeID int64, realNames bool) (cleanup []credit) {
	rows, err := s.db.Query(`SELECT c.user_id, COUNT(*) FROM corrections c JOIN versions v ON v.id=c.version_id
		WHERE v.episode_id=? AND c.user_id<>0 GROUP BY c.user_id ORDER BY COUNT(*) DESC`, episodeID)
	if err != nil {
		return nil
	}
	type row struct {
		id int64
		n  int
	}
	var rs []row
	for rows.Next() {
		var r row
		rows.Scan(&r.id, &r.n)
		rs = append(rs, r)
	}
	rows.Close()
	for _, r := range rs {
		if n := s.creditName(r.id, realNames); n != "" {
			cleanup = append(cleanup, credit{n, r.n})
		}
	}
	return cleanup
}

// creditName: login name for admins, public name for everybody else.
func (s *Store) creditName(userID int64, realNames bool) string {
	u, _, err := s.userByQuery(`id=?`, userID)
	if err != nil {
		return ""
	}
	if realNames {
		return u.Name
	}
	return u.PublicName()
}

func (s *Store) userName(id int64) string {
	var n string
	if s.db.QueryRow(`SELECT name FROM users WHERE id=?`, id).Scan(&n) != nil {
		return ""
	}
	return n
}

// ---------------------------------------------------------------- pages

// handleUserPage: one user's record (admins).
func (s *Server) handleUserPage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	u, _, err := s.st.userByQuery(`id=?`, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderRecord(w, r, u, false)
}

// handleAccount: the logged-in user's own page (password, computers, record).
func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.renderRecord(w, r, currentUser(r), true)
}

func (s *Server) renderRecord(w http.ResponseWriter, r *http.Request, u *User, own bool) {
	title := u.Name
	nav := "users"
	if own {
		title, nav = "Your account", "account"
	}
	s.render(w, r, "user", title, nav, map[string]any{
		"U": u, "Own": own, "Stats": s.st.UserStats(u.ID),
		"Transcribed": s.st.episodesTranscribedBy(u.ID, 200),
		"Cleaned":     s.st.episodesCleanedBy(u.ID, 200),
		"Tokens":      s.st.APITokens(u.ID),
		"Activity":    s.st.Activity(u.ID, 100),
	})
}

// handleActivity: everybody's recent actions (admins).
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "activity", "Who did what", "users", map[string]any{"Activity": s.st.Activity(0, 300)})
}

func (s *Server) handleOwnPassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	_, hash, err := s.st.userByQuery(`id=?`, u.ID)
	if err != nil || !checkPassword(hash, r.FormValue("current")) {
		time.Sleep(700 * time.Millisecond)
		back(w, r, "/account", "", "The current password is wrong.")
		return
	}
	if r.FormValue("new") != r.FormValue("again") {
		back(w, r, "/account", "", "The two new passwords differ.")
		return
	}
	if err := s.st.SetPassword(u.ID, r.FormValue("new")); err != nil {
		back(w, r, "/account", "", err.Error())
		return
	}
	s.st.Audit(u.ID, actPassword, 0, 0, 0, "")
	// SetPassword logged out all sessions: log this browser in again
	if tok, err := s.st.NewSession(u.ID); err == nil {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), Expires: time.Now().Add(sessionTTL)})
	}
	back(w, r, "/account", "Password changed. Other browsers were logged out; connected computers keep working.", "")
}

func (s *Server) handlePublicName(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := s.st.SetPublicName(u.ID, r.FormValue("display"), r.FormValue("show") == "1"); err != nil {
		back(w, r, "/account", "", err.Error())
		return
	}
	back(w, r, "/account", "Saved. Others now see you as \u201c"+s.st.creditName(u.ID, false)+"\u201d.", "")
}

// handleTokenDelete: revoke a computer's key (own ones, or any as admin).
func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	me := currentUser(r)
	uid, _ := strconv.ParseInt(r.FormValue("user"), 10, 64)
	tid, _ := strconv.ParseInt(r.PathValue("tid"), 10, 64)
	to := "/account"
	if uid != me.ID {
		if !me.IsAdmin() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		to = fmt.Sprintf("/users/%d", uid)
	}
	if err := s.st.DeleteAPIToken(uid, tid); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	s.st.Audit(me.ID, actTokenRevoked, 0, 0, 0, fmt.Sprintf("key %d of user %d", tid, uid))
	back(w, r, to, "The computer's key was removed. It has to connect again to help.", "")
}
