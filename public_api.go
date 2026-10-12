package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A small, separate, read-only API for other people's projects to use the
// transcripts - distinct from /api/v1 (which is this app's own internal
// protocol: login, the helper claim/progress/fail/result calls, and the
// in-page search box). /api/public/v1 is versioned on its own, so the
// internal one can keep changing to suit this app without breaking anyone
// else's project, and this one gets an actual stability promise.
//
// It only ever shows what the website already shows a visitor with no
// account: the manual says reading is open to everybody, so this isn't new
// access, just a stable shape for the same data (turn-level: one block of
// text per speaker turn, not the per-word timings the editor itself needs).
// A podcast can be excluded from it even while staying on the website
// (feeds.api_hidden - see feed settings).
//
// A key is required anyway, not to gate the data but so a person using it
// can be told about a breaking change, or have their own key turned off
// without affecting anyone else - see api_access on User and the "public"
// token kind in remote.go. It is never accepted by withUser/TokenUser, so a
// public key can only ever reach these handlers, nothing else on the site.

const apiPageSize = 50

func apiUsageDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// checkRateLimit: is this token still under its daily limit? Also counts the
// call, so callers must check this once per request, not speculatively.
func (s *Store) checkRateLimit(tokenID int64, limit int) (ok bool, used int) {
	day := apiUsageDay(time.Now())
	err := s.db.QueryRow(`INSERT INTO api_usage(token_id,day,count) VALUES(?,?,1)
		ON CONFLICT(token_id,day) DO UPDATE SET count=count+1 RETURNING count`, tokenID, day).Scan(&used)
	if err != nil { // the call itself still goes through; a broken counter must not take the API down
		return true, 0
	}
	return used <= limit, used
}

// publicAPIAuth: the token (and its kind, the user's api_access, and the
// per-token daily limit) for /api/public/v1/*. Writes the error response and
// returns ok=false itself, so a handler can just `if !ok { return }`.
func (s *Server) publicAPIAuth(w http.ResponseWriter, r *http.Request) (u *User, tok APIToken, ok bool) {
	t := bearerToken(r)
	if t == "" || !strings.HasPrefix(t, "qsps_") {
		apiError(w, http.StatusUnauthorized, "an API key is required - see /help#api to get one")
		return nil, tok, false
	}
	h := tokenHash(t)
	var userID int64
	err := s.st.db.QueryRow(`SELECT id,user_id,name,daily_limit FROM api_tokens WHERE token_hash=? AND kind='public'`, h).
		Scan(&tok.ID, &userID, &tok.Name, &tok.DailyLimit)
	if err != nil {
		apiError(w, http.StatusUnauthorized, "that API key isn't valid")
		return nil, tok, false
	}
	u, _, err = s.st.userByQuery(`id=?`, userID)
	if err != nil || !u.MayUsePublicAPI() {
		apiError(w, http.StatusForbidden, "that key's API access was turned off")
		return nil, tok, false
	}
	if ok, used := s.st.checkRateLimit(tok.ID, tok.DailyLimit); !ok {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(tok.DailyLimit))
		w.Header().Set("X-RateLimit-Remaining", "0")
		apiError(w, http.StatusTooManyRequests, "daily limit reached ("+strconv.Itoa(used)+"/"+strconv.Itoa(tok.DailyLimit)+" - resets at midnight UTC)")
		return nil, tok, false
	} else {
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(tok.DailyLimit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(tok.DailyLimit-used))
	}
	if tokenSeenDue(h) {
		s.st.db.Exec(`UPDATE api_tokens SET last_seen=? WHERE id=?`, time.Now().Unix(), tok.ID)
	}
	return u, tok, true
}

// ---------------------------------------------------------------- shapes

type apiTurn struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Speaker string `json:"speaker"`
	Mark    string `json:"mark,omitempty"` // "clip": not the podcast's own people
	Text    string `json:"text"`
}

type apiEpisodeBrief struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	PubDate   int64  `json:"pub_date,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	Status    string `json:"status"`
}

// ---------------------------------------------------------------- handlers

func (s *Server) handlePublicPodcasts(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.publicAPIAuth(w, r); !ok {
		return
	}
	feeds, err := s.st.Feeds()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type podcast struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Language string `json:"language"`
		Episodes int    `json:"episodes_transcribed"`
	}
	var out []podcast
	for _, f := range feeds {
		if s.st.APIHidden(f.ID) {
			continue
		}
		c, _ := s.st.StatusCounts(f.ID)
		out = append(out, podcast{f.ID, f.Title, f.Language, c["done"]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"podcasts": out})
}

// handlePublicEpisodes: an podcast's finished episodes, newest first. ?since=
// (an episode id from a previous call's "cursor") only returns ones touched
// after that - new, or re-corrected, or renamed - for incremental sync.
func (s *Server) handlePublicEpisodes(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.publicAPIAuth(w, r); !ok {
		return
	}
	feedID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad podcast id")
		return
	}
	if s.st.APIHidden(feedID) {
		apiError(w, http.StatusNotFound, "no such podcast")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	eps, total, cursor, err := s.st.publicEpisodes(feedID, since, apiPageSize, (page-1)*apiPageSize)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"episodes": eps, "total": total, "page": page, "per_page": apiPageSize, "cursor": cursor,
	})
}

func (s *Server) handlePublicTranscript(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.publicAPIAuth(w, r); !ok {
		return
	}
	epID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad episode id")
		return
	}
	ep, err := s.st.Episode(epID)
	if err != nil || s.st.APIHidden(ep.FeedID) {
		apiError(w, http.StatusNotFound, "no such episode")
		return
	}
	turns, status, err := s.st.publicTranscript(ep)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status != "done" {
		writeJSON(w, http.StatusOK, map[string]any{
			"episode_id": ep.ID, "title": ep.Title, "pub_date": ep.PubDate, "status": status, "turns": []apiTurn{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"episode_id": ep.ID, "title": ep.Title, "pub_date": ep.PubDate, "status": status, "turns": turns,
	})
}

// handlePublicPersonTurns: everything one person said, across one episode,
// one podcast, or (with neither given) every public podcast - newest first.
func (s *Server) handlePublicPersonTurns(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.publicAPIAuth(w, r); !ok {
		return
	}
	personID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad person id")
		return
	}
	q := r.URL.Query()
	var feedID, epID int64
	if v := q.Get("podcast"); v != "" {
		feedID, _ = strconv.ParseInt(v, 10, 64)
		if s.st.APIHidden(feedID) {
			apiError(w, http.StatusNotFound, "no such podcast")
			return
		}
	}
	if v := q.Get("episode"); v != "" {
		epID, _ = strconv.ParseInt(v, 10, 64)
		if ep, err := s.st.Episode(epID); err != nil || s.st.APIHidden(ep.FeedID) {
			apiError(w, http.StatusNotFound, "no such episode")
			return
		}
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	hits, total, err := browseTurns(s.st, feedID, epID, personID, apiPageSize, (page-1)*apiPageSize)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"turns": hits, "total": total, "page": page, "per_page": apiPageSize})
}

// ---------------------------------------------------------------- store side

func (s *Store) APIHidden(feedID int64) bool {
	var v int
	s.db.QueryRow(`SELECT api_hidden FROM feeds WHERE id=?`, feedID).Scan(&v)
	return v != 0
}

func (s *Store) SetAPIHidden(feedID int64, hidden bool) error {
	_, err := s.db.Exec(`UPDATE feeds SET api_hidden=? WHERE id=?`, hidden, feedID)
	return err
}

func (s *Store) publicEpisodes(feedID, since int64, limit, offset int) ([]apiEpisodeBrief, int, int64, error) {
	where, args := `feed_id=? AND status='done'`, []any{feedID}
	if since > 0 {
		where += ` AND id>?`
		args = append(args, since)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	rows, err := s.db.Query(`SELECT id,title,pub_date,status FROM episodes WHERE `+where+` ORDER BY pub_date DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var out []apiEpisodeBrief
	var maxID int64
	for rows.Next() {
		var e apiEpisodeBrief
		if err := rows.Scan(&e.ID, &e.Title, &e.PubDate, &e.Status); err != nil {
			return nil, 0, 0, err
		}
		e.UpdatedAt = e.PubDate // the simple, honest answer until versions carry a real "last changed" stamp
		if e.ID > maxID {
			maxID = e.ID
		}
		out = append(out, e)
	}
	return out, total, maxID, rows.Err()
}

// publicTranscript: an episode's current transcript as turns (what the
// episode page shows: corrections and spelling fixes applied, ad breaks left
// out). "" status if there is nothing to show yet.
func (s *Store) publicTranscript(ep Episode) ([]apiTurn, string, error) {
	if !ep.ActiveVersionID.Valid {
		return nil, ep.Status, nil
	}
	v, err := s.Version(ep.ActiveVersionID.Int64)
	if err != nil || v.Status != "done" {
		return nil, ep.Status, nil
	}
	segs, toks, turnsRaw, err := s.LoadResults(v.ID)
	if err != nil {
		return nil, "", err
	}
	corr, _ := s.Corrections(v.ID)
	us := applySpelling(buildUtterances(segs, toks, turnsRaw, corr), spellingFor(s, ep.FeedID))
	out := make([]apiTurn, 0, len(us))
	for _, u := range us {
		if u.Mark == markAd {
			continue
		}
		out = append(out, apiTurn{u.StartMs, u.EndMs, labelName(u.Label), u.Mark, u.Text})
	}
	return out, "done", nil
}

// PersonEpisodesIn: the episodes of one podcast where this person actually
// has lines (for the Search page's episode filter, once a podcast and a
// person are both chosen - keeps the dropdown short and meaningful instead
// of listing every episode of the podcast).
func (s *Store) PersonEpisodesIn(feedID, personID int64) ([]apiEpisodeBrief, error) {
	tbl := searchTable()
	rows, err := s.db.Query(`SELECT DISTINCT e.id, e.title, e.pub_date FROM `+tbl+` t JOIN episodes e ON e.id=t.episode_id
		WHERE t.feed_id=? AND t.label=? AND t.mark<>? ORDER BY e.pub_date DESC`, feedID, personLabel(personID), markClip)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []apiEpisodeBrief
	for rows.Next() {
		var e apiEpisodeBrief
		if err := rows.Scan(&e.ID, &e.Title, &e.PubDate); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// browseTurns: the search index's rows (already one speaker each, corrections
// and spelling applied, ads left out), filtered to one person and optionally
// one podcast and/or one episode, without needing search words. Movie clips
// (the person's own voice merged into somebody else's clip, or vice versa)
// are left out - this is "what they said", not "what played under them".
func browseTurns(st *Store, feedID, epID, personID int64, limit, offset int) ([]searchHit, int, error) {
	tbl := searchTable()
	c := tbl + "."
	where := []string{c + "label=?", c + "mark<>?"}
	args := []any{personLabel(personID), markClip}
	if feedID > 0 {
		where = append(where, c+"feed_id=?")
		args = append(args, feedID)
	}
	if epID > 0 {
		where = append(where, c+"episode_id=?")
		args = append(args, epID)
	}
	from := tbl + " JOIN episodes e ON e.id=" + c + "episode_id JOIN feeds f ON f.id=" + c + "feed_id"
	cond := strings.Join(where, " AND ")
	var total int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM `+from+` WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + c + `episode_id, ` + c + `feed_id, e.title, f.title, f.image_file, e.pub_date, ` + c + `start_ms, ` + c + `label, ` + c + `mark, ` + c + `text
		FROM ` + from + ` WHERE ` + cond + ` ORDER BY e.pub_date DESC, ` + c + `start_ms LIMIT ? OFFSET ?`
	rows, err := st.db.Query(q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []searchHit
	for rows.Next() {
		var h searchHit
		var label int
		if err := rows.Scan(&h.EpisodeID, &h.FeedID, &h.Episode, &h.Podcast, &h.Art, &h.PubDate, &h.StartMs, &label, &h.Mark, &h.Text); err != nil {
			return nil, 0, err
		}
		h.Speaker, h.SpClass = labelName(label), spClass(label)
		out = append(out, h)
	}
	return out, total, rows.Err()
}
