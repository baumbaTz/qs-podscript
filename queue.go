package main

// The queue: which episodes come next, in that order. Episodes can be moved
// to the top or the end, or taken out ("not queued" - they stay in the
// podcast and can be put back any time).

import (
	"net/http"
	"strconv"
	"strings"
)

type queueRow struct {
	Pos       int
	EpisodeID int64
	Title     string
	Podcast   string
	FeedID    int64
	PubDate   int64
	Status    string
	Priority  int
	Kind      string // "" / diarize
	Redo      bool   // has a version already
	FirstFeed bool   // its podcast was put first
}

const queueOrder = `ORDER BY e.priority DESC, e.pub_date ASC, e.id ASC`

// queueRows: the waiting episodes in the order they are taken (first limit).
func (s *Store) queueRows(limit int) ([]queueRow, int) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE status IN ('new','queued')`).Scan(&total)
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, f.id, e.pub_date, e.status, e.priority, e.job_kind, e.active_version_id IS NOT NULL
		FROM episodes e JOIN feeds f ON f.id=e.feed_id WHERE e.status IN ('new','queued') `+queueOrder+` LIMIT ?`, limit)
	if err != nil {
		return nil, total
	}
	defer rows.Close()
	first := s.firstFeed()
	var out []queueRow
	for rows.Next() {
		var q queueRow
		rows.Scan(&q.EpisodeID, &q.Title, &q.Podcast, &q.FeedID, &q.PubDate, &q.Status, &q.Priority, &q.Kind, &q.Redo)
		q.Pos = len(out) + 1
		q.FirstFeed = q.FeedID == first && q.Priority > 0
		out = append(out, q)
	}
	return out, total
}

func (s *Server) handleQueuePage(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if r.URL.Query().Get("all") == "1" {
		limit = 100000
	}
	rows, total := s.st.queueRows(limit)
	var skipped int
	s.st.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE status='skipped'`).Scan(&skipped)
	st := getStatus()
	type serverAsk struct {
		ask
		Where string // the server's name
		Link  string // only while that server is the active place
	}
	var serverAsks []serverAsk
	if !s.serverMode {
		act := activePlace(s.st)
		for _, a := range s.st.asks() {
			if !a.Server {
				continue
			}
			sa := serverAsk{ask: a}
			if c, ok := a.server(s.st); ok {
				sa.Where = c.Label()
				if c.ID == act && passThroughAddr() != "" {
					sa.Link = passThroughAddr() + a.Path()
				}
			}
			serverAsks = append(serverAsks, sa)
		}
	}
	s.render(w, r, "queue", "Queue", "queue", map[string]any{
		"Rows": rows, "Total": total, "Skipped": skipped, "Now": st, "HelpersOnly": s.helpersOnly(),
		"ServerAsks": serverAsks,
	})
}

// handleQueueMove: top / end / remove for one waiting episode.
func (s *Server) handleQueueMove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok && r.PathValue("how") != "restore" { // restore: /queue/0/restore
		http.NotFound(w, r)
		return
	}
	to := returnTo(r, "/queue")
	var err error
	msg := ""
	switch r.PathValue("how") {
	case "top":
		var top int
		s.st.db.QueryRow(`SELECT COALESCE(MAX(priority),0) FROM episodes WHERE status IN ('new','queued')`).Scan(&top)
		_, err = s.st.db.Exec(`UPDATE episodes SET priority=? WHERE id=? AND status IN ('new','queued')`, max(top+1, 100), id)
		msg = "Moved to the top – it's next."
	case "end":
		var low int
		s.st.db.QueryRow(`SELECT COALESCE(MIN(priority),0) FROM episodes WHERE status IN ('new','queued')`).Scan(&low)
		_, err = s.st.db.Exec(`UPDATE episodes SET priority=? WHERE id=? AND status IN ('new','queued')`, min(low-1, -1), id)
		msg = "Moved to the end."
	case "remove":
		// never transcribed -> "not queued"; already transcribed -> back to done
		_, err = s.st.db.Exec(`UPDATE episodes SET status=CASE WHEN active_version_id IS NULL THEN 'skipped' ELSE 'done' END,
			priority=0, job_kind='', job_source=0 WHERE id=? AND status IN ('new','queued')`, id)
		msg = "Taken out of the queue. “Transcribe now” on the podcast page puts it back."
	case "restore": // all "not queued" episodes of a podcast back into the queue
		feed, _ := strconv.ParseInt(r.FormValue("feed"), 10, 64)
		_, err = s.st.db.Exec(`UPDATE episodes SET status='new' WHERE status='skipped' AND (feed_id=? OR ?=0)`, feed, feed)
		msg = "Back in the queue."
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, msg, "")
}

func queueWhy(q queueRow) string {
	switch {
	case q.Kind == jobDiarize:
		return "speaker detection only"
	case q.Redo:
		return "transcribe again"
	}
	return ""
}

func queueRank(q queueRow) string {
	switch {
	case q.FirstFeed:
		return "podcast first"
	case q.Priority < 0:
		return "moved to the end"
	}
	return ""
}

// handleQueueOrder: new order of the listed episodes after dragging (JS).
// They get descending priorities above everything that wasn't listed, so
// exactly this order is kept; the rest follows as before.
func (s *Server) handleQueueOrder(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for _, f := range strings.Split(r.FormValue("ids"), ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 100000 {
		http.Error(w, "no order", http.StatusBadRequest)
		return
	}
	tx, err := s.st.db.Begin()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer tx.Rollback()
	// highest priority among the waiting episodes that are NOT in the list
	q := `SELECT COALESCE(MAX(priority),0) FROM episodes WHERE status IN ('new','queued') AND id NOT IN (` +
		strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var base int
	tx.QueryRow(q, args...).Scan(&base)
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE episodes SET priority=? WHERE id=? AND status IN ('new','queued')`, base+1+len(ids)-1-i, id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
