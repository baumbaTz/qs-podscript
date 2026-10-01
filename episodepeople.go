package main

// Who is in an episode. Optional: by default everybody with voice samples can
// be recognized (people on the podcast's list a little more easily). When the
// user sets the people of an episode, recognition only considers them -
// everybody else stays an unnamed voice (a guest, a clip).

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// EpisodePeople: limited = a list was set; in = who is in it.
func (s *Store) EpisodePeople(episodeID int64) (limited bool, in map[int64]bool) {
	in = map[int64]bool{}
	if episodeID <= 0 {
		return false, in
	}
	var l int
	s.db.QueryRow(`SELECT people_limited FROM episodes WHERE id=?`, episodeID).Scan(&l)
	if l == 0 {
		return false, in
	}
	rows, err := s.db.Query(`SELECT person_id FROM episode_people WHERE episode_id=?`, episodeID)
	if err != nil {
		return true, in
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		in[id] = true
	}
	return true, in
}

func (s *Store) SetEpisodePeople(episodeID int64, limited bool, ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM episode_people WHERE episode_id=?`, episodeID); err != nil {
		return err
	}
	if limited {
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO episode_people(episode_id,person_id) VALUES(?,?)`, episodeID, id); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE episodes SET people_limited=? WHERE id=?`, map[bool]int{true: 1, false: 0}[limited], episodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// episodePeopleView: for the episode page.
type episodePeopleView struct {
	Limited bool
	People  []episodePerson
	Summary string
}

type episodePerson struct {
	personOption
	In bool
}

func (s *Store) episodePeopleView(ep Episode) episodePeopleView {
	limited, in := s.EpisodePeople(ep.ID)
	v := episodePeopleView{Limited: limited}
	var names []string
	for _, p := range s.PersonOptions(ep.FeedID) {
		ok := !limited || in[p.ID]
		v.People = append(v.People, episodePerson{p, ok})
		if limited && ok {
			names = append(names, p.Name)
		}
	}
	switch {
	case !limited:
		v.Summary = "everybody known can be recognized"
	case len(names) == 0:
		v.Summary = "nobody known – only unnamed voices"
	default:
		v.Summary = "only " + strings.Join(names, ", ")
	}
	return v
}

// chipPeople: the one-click speaker buttons - the people of the episode if
// set, else the hosts and regulars of the podcast.
func (s *Store) chipPeople(feedID, episodeID int64) (chips, others []personOption) {
	limited, in := s.EpisodePeople(episodeID)
	for _, p := range s.PersonOptions(feedID) {
		if (limited && in[p.ID]) || (!limited && (p.Role == roleHost || p.Role == rolePool)) {
			chips = append(chips, p)
		} else {
			others = append(others, p)
		}
	}
	return
}

func (s *Server) handleEpisodePeople(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ep, err := s.st.Episode(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/episodes/%d#who", id)
	var ids []int64
	limited := true
	switch r.FormValue("preset") {
	case "all":
		limited = false
	case "hosts", "regulars":
		for _, p := range s.st.PersonOptions(ep.FeedID) {
			if p.Role == roleHost || (r.FormValue("preset") == "regulars" && p.Role == rolePool) {
				ids = append(ids, p.ID)
			}
		}
	default:
		r.ParseForm()
		for _, v := range r.Form["person"] {
			if pid, err := strconv.ParseInt(v, 10, 64); err == nil && pid > 0 {
				ids = append(ids, pid)
			}
		}
	}
	if err := s.st.SetEpisodePeople(id, limited, ids); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	msg := "Saved: " + s.st.episodePeopleView(ep).Summary + "."
	// recognize again right away with the new list
	if ep.ActiveVersionID.Valid {
		if v, err := s.st.Version(ep.ActiveVersionID.Int64); err == nil && v.Status == "done" && v.AudioFile != "" {
			if s.worker.Busy() {
				msg += " Something else is running – click “Identify speakers” when it's done."
			} else if err := s.worker.StartIdentify(v); err == nil {
				msg += " Recognizing the speakers again with this list …"
			}
		}
	}
	back(w, r, to, msg, "")
}
