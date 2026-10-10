package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Looking again at older episodes.
//
// The names in a transcript come from comparing every speaker turn with the
// voiceprints of known people (identify.go), and a voiceprint is the average of
// the passages people confirmed. More confirmed passages -> better voiceprints
// -> an episode that was named a month ago could be named better today. The
// server therefore redoes that step by itself, in the background, for episodes
// that are finished:
//
//   - Every version remembers which voice data its automatic names were made
//     with (versions.ident_stamp). A new episode gets its stamp the moment it is
//     identified; episodes from before this feature have none and get one look.
//   - An episode is "stale" when that stamp no longer matches the voice data it
//     would be identified with today – but only if somebody's data changed
//     MEANINGFULLY (by a quarter or more, and at least 20 seconds), so adding
//     one more passage to a person who already has an hour doesn't send the
//     server through every episode again.
//   - It starts only after nobody added a voice sample for six hours (checking
//     sessions are over), when nothing else runs on the server, and goes through
//     the stale episodes one at a time, newest first. This is only the
//     identification step on the server's CPU from the saved audio and the stored
//     voices: no helper computer is involved and the speaker detection itself
//     (who spoke when) is not redone. Names people set by hand are never touched.

const (
	reidentMinGainSec = 20            // a person's voice data counts as changed by at least this many seconds ...
	reidentMinGainPct = 25            // ... and this share of what it was when the episode was last looked at
	reidentQuiet      = 6 * time.Hour // no new voice sample for this long: the checking session is over
	reidentTick       = 30 * time.Minute
	reidentFirstWait  = 2 * time.Minute // after the server starts
	reidentPause      = 3 * time.Second // between two episodes
	reidentRetryFail  = 24 * time.Hour  // an episode that failed is tried again after this
)

// ----------------------------------------------------------------- stamps

type stampPerson struct {
	Sec    int
	Roster bool
}

// identStampFor describes the inputs of identification for one episode: the
// threshold, and every person with voice data that applies (all of them, or the
// ones the episode is limited to) with their seconds and whether they are on the
// podcast's roster (people outside it need more similarity, see identify.go).
func identStampFor(seconds map[int64]int, roster map[int64]string, limited bool, in map[int64]bool, threshold float64) string {
	ids := make([]int64, 0, len(seconds))
	for id := range seconds {
		if limited && !in[id] {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var b strings.Builder
	fmt.Fprintf(&b, "t=%.2f", threshold)
	for _, id := range ids {
		r := "-"
		if _, on := roster[id]; on {
			r = "r"
		}
		fmt.Fprintf(&b, "|%d:%d:%s", id, seconds[id], r)
	}
	return b.String()
}

func parseStamp(s string) (threshold string, people map[int64]stampPerson, ok bool) {
	parts := strings.Split(s, "|")
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "t=") {
		return "", nil, false
	}
	people = map[int64]stampPerson{}
	for _, p := range parts[1:] {
		f := strings.Split(p, ":")
		if len(f) != 3 {
			return "", nil, false
		}
		id, err1 := strconv.ParseInt(f[0], 10, 64)
		sec, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return "", nil, false
		}
		people[id] = stampPerson{sec, f[2] == "r"}
	}
	return parts[0][2:], people, true
}

// stampChanged: were the names made with voice data that differs enough from
// today's to be worth doing again?
func stampChanged(old, cur string) bool {
	if old == cur {
		return false
	}
	if old == "" {
		return true // never looked at since this feature exists
	}
	ot, op, ok1 := parseStamp(old)
	ct, cp, ok2 := parseStamp(cur)
	if !ok1 || !ok2 || ot != ct || len(op) != len(cp) {
		return true // threshold changed, somebody new or gone, or unreadable
	}
	for id, o := range op {
		c, ok := cp[id]
		if !ok || c.Roster != o.Roster {
			return true
		}
		d := c.Sec - o.Sec
		if d < 0 {
			d = -d
		}
		if d >= reidentMinGainSec && d*100 >= o.Sec*reidentMinGainPct {
			return true
		}
	}
	return false
}

// voiceSeconds: confirmed seconds per person (voiceprints of the current model only)
func (s *Store) voiceSeconds() (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT person_id, COALESCE(SUM(seconds),0) FROM voice_samples WHERE model=? GROUP BY person_id`, embeddingFile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]int{}
	for rows.Next() {
		var id int64
		var sec float64
		if err := rows.Scan(&id, &sec); err != nil {
			return nil, err
		}
		m[id] = int(sec + 0.5)
	}
	return m, rows.Err()
}

// lastVoiceChange: when the newest voice sample was added
func (s *Store) lastVoiceChange() time.Time {
	var t int64
	s.db.QueryRow(`SELECT COALESCE(MAX(created_at),0) FROM voice_samples WHERE model=?`, embeddingFile).Scan(&t)
	return time.Unix(t, 0)
}

func (s *Store) identStamp(ep Episode) (string, error) {
	sec, err := s.voiceSeconds()
	if err != nil {
		return "", err
	}
	roster, _ := s.Roster(ep.FeedID)
	limited, in := s.EpisodePeople(ep.ID)
	return identStampFor(sec, roster, limited, in, identifyThreshold(s)), nil
}

// ----------------------------------------------------------------- what is stale

type staleEpisode struct {
	EpisodeID, VersionID int64
	Title                string
}

// staleIdent lists the finished episodes (live version, saved audio) whose
// automatic names were made with other voice data than today's, newest first.
// skip: versions that failed lately.
func (s *Store) staleIdent(skip map[int64]time.Time) ([]staleEpisode, error) {
	sec, err := s.voiceSeconds()
	if err != nil {
		return nil, err
	}
	th := identifyThreshold(s)
	rows, err := s.db.Query(`SELECT e.id, e.feed_id, e.title, v.id, v.ident_stamp
		FROM episodes e JOIN versions v ON v.id=e.active_version_id
		WHERE v.status='done' AND v.audio_file<>'' ORDER BY e.pub_date DESC, e.id DESC`)
	if err != nil {
		return nil, err
	}
	type row struct {
		ep, feed, ver int64
		title, stamp  string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ep, &r.feed, &r.title, &r.ver, &r.stamp); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, r)
	}
	rows.Close()
	rosters := map[int64]map[int64]string{}
	var out []staleEpisode
	for _, r := range all {
		if t, bad := skip[r.ver]; bad && time.Since(t) < reidentRetryFail {
			continue
		}
		ro, ok := rosters[r.feed]
		if !ok {
			ro, _ = s.Roster(r.feed)
			rosters[r.feed] = ro
		}
		limited, in := s.EpisodePeople(r.ep)
		if stampChanged(r.stamp, identStampFor(sec, ro, limited, in, th)) {
			out = append(out, staleEpisode{r.ep, r.ver, r.title})
		}
	}
	return out, nil
}

// ----------------------------------------------------------------- the background job

func autoReidentifyOn(st *Store) bool { return st.Setting("auto_reidentify", "1") == "1" }

// reidentReady: may a pass start / go on now?
func reidentReady(s *Server, since time.Time) bool {
	last := s.st.lastVoiceChange()
	return time.Since(last) >= reidentQuiet && !last.After(since) && !s.worker.Busy()
}

func runReidentify(ctx context.Context, s *Server) {
	failed := map[int64]time.Time{}
	wait := reidentFirstWait
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = reidentTick
		reidentifyPass(ctx, s, failed)
	}
}

// reidentifyPass: one go through the stale episodes, until none is left, the
// switch is turned off, new voice data arrives or the server has other work.
func reidentifyPass(ctx context.Context, s *Server, failed map[int64]time.Time) {
	if !autoReidentifyOn(s.st) {
		return
	}
	started := time.Now()
	if !reidentReady(s, started) {
		return
	}
	todo, err := s.st.staleIdent(failed)
	if err != nil || len(todo) == 0 {
		return
	}
	logf("Looking again at the speaker names of older episodes: %d to do", len(todo))
	done := 0
	for i, t := range todo {
		if ctx.Err() != nil || !autoReidentifyOn(s.st) || !reidentReady(s, started) {
			break
		}
		v, err := s.st.Version(t.VersionID)
		if err == nil {
			_, err = os.Stat(filepath.Join(P.Audio, v.AudioFile))
		}
		if err == nil {
			ectx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			var res identifyResult
			res, err = identifyVersion(ectx, s.st, v, nil)
			cancel()
			if err == nil {
				done++
				logf("   %q: %s (%d left)", t.Title, res, len(todo)-i-1)
			}
		}
		if err != nil {
			failed[t.VersionID] = time.Now()
			logf("   %q: speaker names not renewed: %v", t.Title, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(reidentPause):
		}
	}
	if done > 0 {
		logf("Renewed the speaker names of %d older episode(s)", done)
	}
}

// ----------------------------------------------------------------- Setup

type reidentInfo struct {
	On       bool
	Waiting  int
	QuietH   int
	GainPct  int
	GainSec  int
	Blocking string // why nothing happens right now ("" = nothing in the way)
}

func (s *Server) reidentSummary() reidentInfo {
	info := reidentInfo{On: autoReidentifyOn(s.st), QuietH: int(reidentQuiet / time.Hour), GainPct: reidentMinGainPct, GainSec: reidentMinGainSec}
	if list, err := s.st.staleIdent(nil); err == nil {
		info.Waiting = len(list)
	}
	if info.Waiting > 0 && info.On {
		if left := reidentQuiet - time.Since(s.st.lastVoiceChange()); left > 0 {
			info.Blocking = fmt.Sprintf("Somebody confirmed a voice recently – it starts in about %d hour(s) if nobody adds more.", int(left/time.Hour)+1)
		} else if s.worker.Busy() {
			info.Blocking = "This server is busy with another job right now."
		}
	}
	return info
}

func (s *Server) handleReidentifySettings(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("on") == "1"
	s.st.SetSetting("auto_reidentify", map[bool]string{true: "1", false: "0"}[on])
	if on {
		back(w, r, "/setup#reidentify", "Saved: older episodes are looked at again automatically.", "")
		return
	}
	back(w, r, "/setup#reidentify", "Saved: the server no longer renews speaker names by itself.", "")
}
