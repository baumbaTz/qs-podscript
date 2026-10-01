package main

// Removing a whole podcast, and putting a podcast first in the queue.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// DeleteFeed removes a podcast with its episodes, versions, corrections and
// audio copies. People and their voice samples stay (they are about the
// people, and may be on other podcasts too).
func (s *Store) DeleteFeed(feedID int64) (episodes int, err error) {
	var busy int
	s.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE feed_id=? AND status IN ('processing','leased')`, feedID).Scan(&busy)
	if busy > 0 {
		return 0, fmt.Errorf("an episode of this podcast is being transcribed right now – stop that first (or wait until it's done)")
	}
	var art string
	s.db.QueryRow(`SELECT image_file FROM feeds WHERE id=?`, feedID).Scan(&art)
	defer removeArtwork(art)
	var files []string
	rows, err := s.db.Query(`SELECT DISTINCT v.audio_file FROM versions v JOIN episodes e ON e.id=v.episode_id
		WHERE e.feed_id=? AND v.audio_file!=''`, feedID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var f string
		rows.Scan(&f)
		files = append(files, f)
	}
	rows.Close()
	s.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE feed_id=?`, feedID).Scan(&episodes)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM spelling WHERE feed_id=?`, // fixes only for this podcast
		`DELETE FROM feeds WHERE id=?`,         // episodes, versions, corrections … follow (foreign keys)
	} {
		if _, err := tx.Exec(q, feedID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, f := range files {
		if f == filepath.Base(f) {
			os.Remove(filepath.Join(P.Audio, f))
		}
	}
	return episodes, nil
}

func (s *Server) handleFeedDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := s.st.Feed(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("confirm") != "1" {
		back(w, r, fmt.Sprintf("/feeds/%d#remove", id), "", "Tick the box to confirm that the podcast and all its transcripts should be removed.")
		return
	}
	n, err := s.st.DeleteFeed(id)
	if err != nil {
		back(w, r, fmt.Sprintf("/feeds/%d#remove", id), "", err.Error())
		return
	}
	logf("Podcast %d (%s) removed with %d episodes", id, f.Title, n)
	back(w, r, "/", fmt.Sprintf("“%s” was removed (%d episodes).", f.Title, n), "")
}

// topPriority (SQL): above every waiting episode - "next", whatever was
// moved before.
const topPriority = `MAX(100, COALESCE((SELECT MAX(priority) FROM episodes WHERE status IN ('new','queued')),0)+1)`

// The podcast put first ("Transcribe this podcast next") is remembered in
// the setting first_feed; its waiting episodes get the top priority.
func (s *Store) firstFeed() int64 {
	id, _ := strconv.ParseInt(s.Setting("first_feed", "0"), 10, 64)
	return id
}

func (s *Store) feedIsFirst(feedID int64) bool {
	if s.firstFeed() != feedID {
		return false
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE feed_id=? AND status IN ('new','queued') AND priority>0`, feedID).Scan(&n)
	return n > 0
}

// putFeedFirst: all waiting episodes of the podcast go to the top (among
// themselves oldest first); the podcast that was first before goes back.
func (s *Store) putFeedFirst(feedID int64) int64 {
	if old := s.firstFeed(); old != 0 && old != feedID {
		s.db.Exec(`UPDATE episodes SET priority=0 WHERE feed_id=? AND status IN ('new','queued') AND priority>0`, old)
	}
	var top int
	s.db.QueryRow(`SELECT COALESCE(MAX(priority),0) FROM episodes WHERE status IN ('new','queued')`).Scan(&top)
	res, _ := s.db.Exec(`UPDATE episodes SET priority=? WHERE feed_id=? AND status IN ('new','queued')`, max(top+1, 100), feedID)
	s.SetSetting("first_feed", strconv.FormatInt(feedID, 10))
	n, _ := res.RowsAffected()
	return n
}

func (s *Store) unputFeedFirst(feedID int64) {
	s.db.Exec(`UPDATE episodes SET priority=0 WHERE feed_id=? AND status IN ('new','queued') AND priority>0`, feedID)
	if s.firstFeed() == feedID {
		s.SetSetting("first_feed", "0")
	}
}

// handleFeedFirst: this podcast's waiting episodes come next in the queue -
// also for a queue that is already running. If nothing runs, it starts.
func (s *Server) handleFeedFirst(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/feeds/%d", id)
	if r.FormValue("undo") == "1" {
		s.st.unputFeedFirst(id)
		back(w, r, to, "Back to the normal order (oldest episodes of all podcasts first).", "")
		return
	}
	n := s.st.putFeedFirst(id)
	if s.helpersOnly() {
		back(w, r, to, fmt.Sprintf("This podcast's %d waiting episode(s) are now first in line for the helpers.", n), "")
		return
	}
	if s.worker.Busy() {
		back(w, r, to, fmt.Sprintf("This podcast goes next: its %d waiting episode(s) come right after the current episode.", n), "")
		return
	}
	if !getSetupState(s.st).Ready() {
		back(w, r, "/setup", "", "Finish the setup first.")
		return
	}
	if err := s.worker.StartQueue(queueOpts{Refresh: true}); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, fmt.Sprintf("Transcribing started with this podcast (%d waiting episodes), then the others.", n), "")
}
