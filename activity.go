package main

import (
	"fmt"
	"net/http"
	"sort"
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
	Transcribed    int   // episodes transcribed on their computers (not counting redone speaker detection)
	TranscribedSec int64 // audio length of those
	Speakers       int   // speaker detection redone on their computers
	BusySec        int64 // their computers' time for all of it
	WhisperSec     int64 // ... of that: transcribing
	DiarizeSec     int64 // ... speaker detection
	Computers      []computerWork
	CleanedEps     int // episodes with corrections by them
	Corrections    int
	Checks         int // passages checked in "Look Who's Talking"
	LastActive     int64
}

func (s *Store) UserStats(userID int64) UserStats {
	var st UserStats
	work := s.computerWorkBy(userID)
	var secs float64
	byDev := map[string]*computerWork{}
	for _, w := range work {
		if w.Kind == workTranscribe {
			st.Transcribed++
			secs += w.AudioSec
		} else {
			st.Speakers++
		}
		st.BusySec += w.BusySec
		st.WhisperSec += w.WhisperSec
		st.DiarizeSec += w.DiarizeSec
		c := byDev[w.Device]
		if c == nil {
			c = &computerWork{Name: w.Device}
			byDev[w.Device] = c
		}
		c.Jobs++
		c.AudioSec += int64(w.AudioSec)
		c.BusySec += w.BusySec
		c.WhisperSec += w.WhisperSec
		c.DiarizeSec += w.DiarizeSec
		c.Last = max(c.Last, w.At)
	}
	st.TranscribedSec = int64(secs)
	for _, c := range byDev {
		st.Computers = append(st.Computers, *c)
	}
	sort.Slice(st.Computers, func(i, j int) bool { return st.Computers[i].BusySec > st.Computers[j].BusySec })
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

// ---------------------------------------------------------------- computer time
//
// Each version keeps how long its steps took ("download 4s, whisper 6m12s,
// diarize 49m3s, …"). The time goes to whoever's computer did the work: the
// transcriber, or for redone speaker detection the helper that did it
// (speakers_by, since 0.31.0 - older redone versions by helpers can't be
// told apart and are left out).

const (
	workTranscribe = "transcribed"
	workSpeakers   = "speakers"
)

type computerJob struct {
	EpisodeID  int64
	Title      string
	Podcast    string
	At         int64
	Device     string
	Kind       string // workTranscribe | workSpeakers
	AudioSec   float64
	BusySec    int64
	WhisperSec int64
	DiarizeSec int64
}

// Speed: "12×" = 12 minutes of audio per minute of work.
func (j computerJob) Speed() string { return speedText(j.AudioSec, j.BusySec) }

type computerWork struct {
	Name                                      string
	Jobs                                      int
	AudioSec, BusySec, WhisperSec, DiarizeSec int64
	Last                                      int64
}

func (c computerWork) Speed() string { return speedText(float64(c.AudioSec), c.BusySec) }

func speedText(audio float64, busy int64) string {
	if busy <= 0 || audio <= 0 {
		return ""
	}
	x := audio / float64(busy)
	if x >= 10 {
		return fmt.Sprintf("%.0f×", x)
	}
	return fmt.Sprintf("%.1f×", x)
}

// timingSteps reads a version's timing text into seconds per step.
func timingSteps(t string) (total, whisper, diarize int64) {
	for _, part := range strings.Split(t, ",") {
		f := strings.Fields(part)
		if len(f) != 2 {
			continue
		}
		d, err := time.ParseDuration(f[1])
		if err != nil || d < 0 {
			continue
		}
		sec := int64(d.Seconds() + 0.5)
		switch f[0] {
		case "download", "convert", "identify", "audio":
		case "whisper":
			whisper += sec
		case "diarize":
			diarize += sec
		default:
			continue
		}
		total += sec
	}
	return
}

// computerWorkBy: everything this user's computers did, newest first.
func (s *Store) computerWorkBy(userID int64) []computerJob {
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, v.created_at, v.transcribed_by, v.transcribed_on, v.speakers_by, v.speakers_on,
			v.audio_seconds, v.timing
		FROM versions v JOIN episodes e ON e.id=v.episode_id JOIN feeds f ON f.id=e.feed_id
		WHERE (v.transcribed_by=? OR v.speakers_by=?) AND v.status='done' ORDER BY v.created_at DESC, v.id DESC`, userID, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []computerJob
	for rows.Next() {
		var j computerJob
		var tBy, sBy int64
		var tOn, sOn, timing string
		if rows.Scan(&j.EpisodeID, &j.Title, &j.Podcast, &j.At, &tBy, &tOn, &sBy, &sOn, &j.AudioSec, &timing) != nil {
			continue
		}
		redo := strings.HasPrefix(timing, "transcript from ")
		switch {
		case sBy != 0: // speakers by a helper
			if sBy != userID {
				continue
			}
			j.Kind, j.Device = workSpeakers, sOn
		case redo:
			if strings.Contains(timing, "speakers by ") {
				continue // by a helper before 0.31.0: unknown whose
			}
			j.Kind, j.Device = workSpeakers, tOn
		default:
			j.Kind, j.Device = workTranscribe, tOn
		}
		if j.Device == "" {
			j.Device = "(unnamed)"
		}
		j.BusySec, j.WhisperSec, j.DiarizeSec = timingSteps(timing)
		out = append(out, j)
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
	data := map[string]any{
		"U": u, "Own": own, "Stats": s.st.UserStats(u.ID),
		"Work":     firstN(s.st.computerWorkBy(u.ID), 200),
		"Cleaned":  s.st.episodesCleanedBy(u.ID, 200),
		"Tokens":   s.st.APITokens(u.ID),
		"Activity": s.st.Activity(u.ID, 100),
	}
	if !own {
		// rights and password of this user (admins). "Select all" / "Select none"
		// only pre-tick the boxes of this page (nothing is saved before "Save")
		feeds, _ := s.st.Feeds() // alphabetical
		rights, preset := u.Podcasts, ""
		switch r.URL.Query().Get("preset") {
		case "all":
			rights, preset = map[int64]bool{}, "all"
			for _, f := range feeds {
				rights[f.ID] = true
			}
		case "none":
			rights, preset = map[int64]bool{}, "none"
		}
		all := len(feeds) > 0
		for _, f := range feeds {
			if !rights[f.ID] {
				all = false
			}
		}
		data["Feeds"], data["Rights"], data["AllRights"], data["Preset"] = feeds, rights, all, preset
	}
	s.render(w, r, "user", title, nav, data)
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

func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}
