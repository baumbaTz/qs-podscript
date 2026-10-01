package main

// "Asked for": episodes somebody clicked "Transcribe again" / "Redo speaker
// detection" for while this computer was busy - on its own pages or on a
// server page opened through it - and whole server podcasts ("Transcribe
// this podcast" on a server page). They are done right after the current work
// (and first when "Start transcribing" is pressed). Stopping keeps the list;
// nothing else starts by itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type ask struct {
	Server bool    `json:"server,omitempty"` // an episode on a saved server ...
	SrvID  int64   `json:"srv,omitempty"`    // ... this one (0: from before 0.26 = the first one)
	ID     int64   `json:"id"`
	Kind   string  `json:"kind,omitempty"` // "" transcribe | "diarize" | "podcast" (ID = the podcast: all its waiting episodes)
	Title  string  `json:"title,omitempty"`
	At     int64   `json:"at"`
	Skip   []int64 `json:"skip,omitempty"` // podcast: episodes that failed here during this run
}

// same: the same episode in the same place
func (a ask) same(b ask) bool { return a.Server == b.Server && a.SrvID == b.SrvID && a.ID == b.ID }

// server: the saved server of a server entry.
func (a ask) server(st *Store) (remoteConf, bool) {
	if a.SrvID == 0 { // listed before 0.26.0
		if list := savedServers(st); len(list) > 0 {
			return list[0], list[0].Connected()
		}
		return remoteConf{}, false
	}
	return serverByID(st, a.SrvID)
}

func (a ask) What() string {
	switch a.Kind {
	case "diarize":
		return "Redo speaker detection"
	case askPodcast:
		return "Transcribe this podcast (all its waiting episodes)"
	}
	return "Transcribe again"
}

const askPodcast = "podcast"

// minFeedClaim: servers that can hand out "the next episode of podcast X".
const minFeedClaim = "0.26.1"

// Path of the asked-for thing on the server.
func (a ask) Path() string {
	if a.Kind == askPodcast {
		return fmt.Sprintf("/feeds/%d", a.ID)
	}
	return fmt.Sprintf("/episodes/%d", a.ID)
}

var asksMu sync.Mutex

func (s *Store) asks() []ask {
	var out []ask
	json.Unmarshal([]byte(s.Setting("asks", "[]")), &out)
	return out
}

func (s *Store) saveAsks(a []ask) {
	b, _ := json.Marshal(a)
	s.SetSetting("asks", string(b))
}

// addAsk appends (or updates) an entry; the same episode is listed once.
func (s *Store) addAsk(a ask) {
	asksMu.Lock()
	defer asksMu.Unlock()
	list := s.asks()
	for i, o := range list {
		if o.same(a) {
			list[i].Kind = a.Kind
			s.saveAsks(list)
			return
		}
	}
	a.At = time.Now().Unix()
	s.saveAsks(append(list, a))
}

// addAskSkip notes an episode that failed during a podcast run.
func (s *Store) addAskSkip(a ask, episodeID int64) {
	asksMu.Lock()
	defer asksMu.Unlock()
	list := s.asks()
	for i := range list {
		if list[i].same(a) && len(list[i].Skip) < 500 {
			list[i].Skip = append(list[i].Skip, episodeID)
			s.saveAsks(list)
			return
		}
	}
}

func (s *Store) dropAsk(a ask) {
	asksMu.Lock()
	defer asksMu.Unlock()
	list := s.asks()
	out := list[:0]
	for _, o := range list {
		if !o.same(a) {
			out = append(out, o)
		}
	}
	s.saveAsks(out)
}

// dropServerAsks removes the entries of one server (0: of all servers).
func (s *Store) dropServerAsks(srv int64) {
	asksMu.Lock()
	defer asksMu.Unlock()
	var out []ask
	for _, o := range s.asks() {
		if !o.Server || (srv != 0 && o.SrvID != srv && o.SrvID != 0) {
			out = append(out, o)
		}
	}
	s.saveAsks(out)
}

// processAsk does the first entry of the list. did = false: list empty.
// claimErr: the server couldn't be asked (the entry stays).
func processAsk(ctx context.Context, st *Store, keepWork bool) (did bool, err error, claimErr error) {
	for {
		list := st.asks()
		if len(list) == 0 {
			return false, nil, nil
		}
		a := list[0]
		if a.Server {
			c, ok := a.server(st)
			if !ok {
				st.dropAsk(a) // server removed meanwhile
				continue
			}
			var want any = map[string]int64{"episode_id": a.ID}
			if a.Kind == askPodcast {
				if v := seenServerVersion(c); !versionAtLeast(v, minFeedClaim) {
					logf("%s: the server needs QS-PodScript %s or newer for \"Transcribe this podcast\" from your computer - taken off the list", c.Label(), minFeedClaim)
					st.dropAsk(a)
					continue
				}
				want = claimFilter{FeedID: a.ID, Skip: a.Skip}
			}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			var claim struct {
				Job *Job `json:"job"`
			}
			cerr := apiCall(cctx, c, http.MethodPost, "/api/v1/work/claim", want, &claim)
			cancel()
			if cerr != nil {
				if se, ok := cerr.(*apiStatusError); ok && se.permanent() {
					logf("Server episode %d: %v - taken off the list", a.ID, cerr)
					st.dropAsk(a)
					continue
				}
				return false, nil, cerr
			}
			if a.Kind == askPodcast {
				// stays on the list until the podcast has nothing waiting
				if claim.Job == nil {
					st.dropAsk(a)
					logf("%s: nothing (more) waiting in %s", c.Label(), orStr(a.Title, fmt.Sprintf("podcast %d", a.ID)))
					continue
				}
				err := runRemoteJob(ctx, st, c, claim.Job)
				if err != nil && ctx.Err() == nil {
					// failed here: let other computers try it, go on with the next one
					st.addAskSkip(a, claim.Job.EpisodeID)
				}
				return true, err, nil
			}
			st.dropAsk(a)
			if claim.Job == nil {
				logf("Server episode %d: already taken by another computer or done", a.ID)
				continue
			}
			return true, runRemoteJob(ctx, st, c, claim.Job), nil
		}
		// own episode: still waiting in the queue?
		st.dropAsk(a)
		ep, eerr := st.Episode(a.ID)
		if eerr != nil || (ep.Status != "queued" && ep.Status != "new") {
			continue // taken out of the queue or already done meanwhile
		}
		return true, processQueued(ctx, st, ep, keepWork), nil
	}
}

// processQueued does one waiting episode of this computer's queue: a full
// transcription, or only the speaker detection when that was asked for.
func processQueued(ctx context.Context, st *Store, ep Episode, keepWork bool) error {
	var kind string
	var source int64
	st.db.QueryRow(`SELECT job_kind, job_source FROM episodes WHERE id=?`, ep.ID).Scan(&kind, &source)
	if kind != "diarize" {
		return processEpisode(ctx, st, ep, keepWork)
	}
	// the episode itself stays as it is; a new version comes on top
	st.db.Exec(`UPDATE episodes SET status=CASE WHEN active_version_id IS NULL THEN 'new' ELSE 'done' END,
		priority=0, job_kind='', job_source=0 WHERE id=?`, ep.ID)
	src, err := st.Version(source)
	if err != nil {
		return fmt.Errorf("episode %d: version %d not found", ep.ID, source)
	}
	err = rediarizeVersion(ctx, st, src)
	if ctx.Err() != nil {
		st.QueueRediarize(ep.ID, source) // stopped: stays in line
	}
	return err
}

// handleAskDrop: take one entry off the list (queue page).
func (s *Server) handleAskDrop(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	srv, _ := strconv.ParseInt(r.FormValue("srv"), 10, 64)
	s.st.dropAsk(ask{Server: true, SrvID: srv, ID: id}) // own episodes are taken out in the queue itself
	back(w, r, "/queue", "Taken off the list – the server episode stays in line for the helpers.", "")
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
