package main

// "Look Who's Talking Now" (was "Who's talking?") – checking speakers in small pieces.
//
// Instead of a whole episode, a helper gets one passage of about half a
// minute: the audio of just that part and its text, cut into short rows
// (sentences, at most ~12 s). For every row they confirm the speaker or pick
// another one. Changes become ordinary speaker corrections (undoable on the
// episode page, carried to new versions like any other); the passage is
// recorded as checked. Passages that already have manual speaker corrections
// count as checked too.

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	quizRowMaxMs    = 12000 // a row is at most this long …
	quizRowMinMs    = 2500  // … and ends at a sentence end once it is this long
	quizWinTargetMs = 25000 // a passage ends at the next speaker change after this
	quizWinMaxMs    = 40000 // … and at the latest here
	quizGapMs       = 5000  // silence/music longer than this ends a passage
	quizCheckedPart = 0.8   // a passage counts as checked when this much is covered
	quizReserve     = 10 * time.Minute
	quizSkipFor     = 30 * time.Minute
)

type quizRow struct {
	StartMs, EndMs int64
	Label          int
	Words          []Word
}

type quizWindow struct {
	StartMs, EndMs int64
	Rows           []quizRow
	Score          int
}

func (w quizWindow) key(vid int64) string { return fmt.Sprintf("%d:%d", vid, w.StartMs) }

// quizRows cuts the utterances into short rows. Ads and movie clips are left out.
func quizRows(us []Utterance) []quizRow {
	var rows []quizRow
	for _, u := range us {
		if u.Mark != "" || len(u.Words) == 0 {
			continue
		}
		var cur []Word
		flush := func() {
			if len(cur) > 0 {
				rows = append(rows, quizRow{StartMs: cur[0].StartMs, EndMs: cur[len(cur)-1].EndMs, Label: u.Label, Words: cur})
				cur = nil
			}
		}
		for _, w := range u.Words {
			if len(cur) > 0 && w.EndMs-cur[0].StartMs > quizRowMaxMs {
				flush()
			}
			cur = append(cur, w)
			t := strings.TrimSpace(w.Text)
			if strings.HasSuffix(t, ".") || strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!") {
				if w.EndMs-cur[0].StartMs >= quizRowMinMs {
					flush()
				}
			}
		}
		flush()
	}
	return rows
}

// quizWindows groups rows into passages of about half a minute, preferably
// ending at a speaker change.
func quizWindows(rows []quizRow) []quizWindow {
	var out []quizWindow
	var cur []quizRow
	flush := func() {
		if len(cur) == 0 {
			return
		}
		w := quizWindow{StartMs: cur[0].StartMs, EndMs: cur[len(cur)-1].EndMs, Rows: cur}
		w.Score = quizScore(cur)
		out = append(out, w)
		cur = nil
	}
	for _, r := range rows {
		if len(cur) > 0 {
			span := r.EndMs - cur[0].StartMs
			last := cur[len(cur)-1]
			switch {
			case r.StartMs-last.EndMs > quizGapMs, span > quizWinMaxMs:
				flush()
			case last.EndMs-cur[0].StartMs >= quizWinTargetMs && r.Label != last.Label:
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	// a very short last passage joins the one before (if that stays small enough)
	if n := len(out); n >= 2 && out[n-1].EndMs-out[n-1].StartMs < 8000 && out[n-1].EndMs-out[n-2].StartMs <= quizWinMaxMs+8000 &&
		out[n-1].StartMs-out[n-2].EndMs <= quizGapMs {
		out[n-2].Rows = append(out[n-2].Rows, out[n-1].Rows...)
		out[n-2].EndMs = out[n-1].EndMs
		out[n-2].Score = quizScore(out[n-2].Rows)
		out = out[:n-1]
	}
	return out
}

// quizScore: which passages to ask about first - unnamed voices, speaker
// changes, crosstalk and "unknown" are where help matters most.
func quizScore(rows []quizRow) int {
	score, changes := 0, 0
	unnamed := false
	for i, r := range rows {
		if r.Label >= 0 && r.Label < personLabelBase {
			unnamed = true
		}
		if r.Label == labelCrosstalk || r.Label == labelUnknown {
			score++
		}
		if i > 0 && r.Label != rows[i-1].Label {
			changes++
		}
	}
	if unnamed {
		score += 3
	}
	return score + min(changes, 3)
}

// ---------------------------------------------------------------- checked passages

// checkedIntervals: passages someone confirmed, plus passages with manual
// speaker corrections (fixed on the episode page = looked at).
func (s *Store) checkedIntervals(vid int64, corr []Correction) []interval {
	var ivs []interval
	rows, err := s.db.Query(`SELECT start_ms, end_ms FROM checks WHERE version_id=?`, vid)
	if err == nil {
		for rows.Next() {
			var iv interval
			rows.Scan(&iv.a, &iv.b)
			ivs = append(ivs, iv)
		}
		rows.Close()
	}
	for _, c := range corr {
		if c.Kind == "range" && !c.Auto && c.Origin != originCarried && c.EndMs > c.StartMs {
			ivs = append(ivs, interval{c.StartMs, c.EndMs})
		}
	}
	return mergeIntervals(ivs)
}

func mergeIntervals(ivs []interval) []interval {
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].a < ivs[j].a })
	var out []interval
	for _, iv := range ivs {
		if n := len(out); n > 0 && iv.a <= out[n-1].b {
			out[n-1].b = max(out[n-1].b, iv.b)
			continue
		}
		out = append(out, iv)
	}
	return out
}

// covered: how much of a..b lies inside the (merged) intervals.
func covered(ivs []interval, a, b int64) int64 {
	var n int64
	for _, iv := range ivs {
		lo, hi := max(a, iv.a), min(b, iv.b)
		if hi > lo {
			n += hi - lo
		}
	}
	return n
}

func (w quizWindow) checked(ivs []interval) bool {
	span := w.EndMs - w.StartMs
	return span <= 0 || float64(covered(ivs, w.StartMs, w.EndMs)) >= quizCheckedPart*float64(span)
}

// quizProgress: total and checked length of all passages of a version.
func quizProgress(wins []quizWindow, ivs []interval) (total, done int64) {
	for _, w := range wins {
		total += w.EndMs - w.StartMs
		if w.checked(ivs) {
			done += w.EndMs - w.StartMs
		}
	}
	return
}

func (s *Store) saveQuizProgress(vid, total, done int64) {
	// only when something changed: page views shouldn't write to the database
	s.db.Exec(`UPDATE versions SET quiz_total_ms=?, quiz_done_ms=? WHERE id=? AND (quiz_total_ms!=? OR quiz_done_ms!=?)`,
		total, done, vid, total, done)
}

// quizState: everything needed to ask about one version.
type quizState struct {
	Version Version
	FeedID  int64
	Windows []quizWindow
	Checked []interval
	Utts    []Utterance
}

func (s *Store) loadQuiz(vid int64) (*quizState, error) {
	v, err := s.Version(vid)
	if err != nil {
		return nil, err
	}
	var feedID int64
	if err := s.db.QueryRow(`SELECT feed_id FROM episodes WHERE id=?`, v.EpisodeID).Scan(&feedID); err != nil {
		return nil, err
	}
	segs, toks, turns, err := s.LoadResults(vid)
	if err != nil {
		return nil, err
	}
	corr, _ := s.Corrections(vid)
	us := applySpelling(buildUtterances(segs, toks, turns, corr), spellingFor(s, feedID))
	q := &quizState{Version: v, FeedID: feedID, Utts: us}
	q.Windows = quizWindows(quizRows(us))
	q.Checked = s.checkedIntervals(vid, corr)
	total, done := quizProgress(q.Windows, q.Checked)
	s.saveQuizProgress(vid, total, done)
	return q, nil
}

// ---------------------------------------------------------------- reservations
// So two helpers don't get the same passage at the same time. In memory only.

type quizHold struct {
	owner string
	skip  bool
	until time.Time
}

var quizHolds = struct {
	sync.Mutex
	m map[string]quizHold
}{m: map[string]quizHold{}}

func quizAvailable(key, owner string) bool {
	quizHolds.Lock()
	defer quizHolds.Unlock()
	h, ok := quizHolds.m[key]
	if !ok || time.Now().After(h.until) {
		return true
	}
	return h.owner == owner && !h.skip
}

func quizHoldSet(key, owner string, skip bool) {
	quizHolds.Lock()
	defer quizHolds.Unlock()
	now := time.Now()
	for k, h := range quizHolds.m { // tidy up
		if now.After(h.until) {
			delete(quizHolds.m, k)
		}
	}
	d := quizReserve
	if skip {
		d = quizSkipFor
	}
	quizHolds.m[key] = quizHold{owner, skip, now.Add(d)}
}

func quizRelease(key string) {
	quizHolds.Lock()
	delete(quizHolds.m, key)
	quizHolds.Unlock()
}

func (s *Server) quizOwner(r *http.Request) string {
	if u := currentUser(r); u != nil {
		return fmt.Sprintf("u%d", u.ID)
	}
	return "local"
}

// ---------------------------------------------------------------- picking

// quizCandidates: active versions of a podcast (or one episode) that still
// have unchecked passages, in random order.
func (s *Store) quizCandidates(feedID, episodeID int64) []int64 {
	var rows *sql.Rows
	var err error
	base := `SELECT v.id FROM episodes e JOIN versions v ON v.id=e.active_version_id
		WHERE e.status='done' AND v.status='done' AND v.audio_file!='' AND (v.quiz_total_ms=0 OR v.quiz_done_ms < v.quiz_total_ms)`
	if episodeID > 0 {
		rows, err = s.db.Query(base+` AND e.id=?`, episodeID)
	} else {
		rows, err = s.db.Query(base+` AND e.feed_id=? ORDER BY RANDOM() LIMIT 12`, feedID)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// pickQuiz finds the next passage to ask about.
func (s *Server) pickQuiz(feedID, episodeID int64, owner string) (*quizState, *quizWindow) {
	for _, vid := range s.st.quizCandidates(feedID, episodeID) {
		q, err := s.st.loadQuiz(vid)
		if err != nil || !fileExists(filepath.Join(P.Audio, q.Version.AudioFile)) {
			continue
		}
		var open []quizWindow
		for _, w := range q.Windows {
			if !w.checked(q.Checked) && quizAvailable(w.key(vid), owner) {
				open = append(open, w)
			}
		}
		if len(open) == 0 {
			continue
		}
		sort.SliceStable(open, func(i, j int) bool { return open[i].Score > open[j].Score })
		top := 0
		for top < len(open) && top < 4 && open[top].Score == open[0].Score {
			top++
		}
		w := open[rand.Intn(top)]
		quizHoldSet(w.key(vid), owner, false)
		return q, &w
	}
	return nil, nil
}

// ---------------------------------------------------------------- pages

type quizChoice struct {
	Value, Name, Class string
}

type quizRowView struct {
	I              int
	StartMs, EndMs int64
	Offset         string // position inside the passage, "0:12"
	Current        string // value of the current speaker
	CurrentName    string
	Class          string
	Words          []Word
	Choices        []quizChoice // chips; the current one is among them
}

func labelValue(l int) string {
	if id, ok := labelPerson(l); ok {
		return fmt.Sprintf("p%d", id)
	}
	switch l {
	case labelCrosstalk:
		return "x"
	case labelUnknown:
		return "u"
	}
	return strconv.Itoa(l)
}

func (s *Server) handleQuiz(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var feedID, episodeID int64
	var scope string
	if strings.HasPrefix(r.URL.Path, "/episodes/") {
		episodeID = id
		ep, err := s.st.Episode(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		feedID = ep.FeedID
		scope = fmt.Sprintf("/episodes/%d/quiz", id)
	} else {
		feedID = id
		scope = fmt.Sprintf("/feeds/%d/quiz", id)
	}
	feed, err := s.st.Feed(feedID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	owner := s.quizOwner(r)
	if sk := r.URL.Query().Get("skip"); sk != "" {
		quizHoldSet(sk, owner, true)
	}
	data := map[string]any{"Feed": feed, "Scope": scope, "EpisodeScope": episodeID > 0}
	data["Progress"] = s.st.feedQuizProgress(feedID)
	data["Mine"] = s.st.checksBy(s.userID(r))
	q, win := s.pickQuiz(feedID, episodeID, owner)
	if q == nil {
		data["Done"] = true
		data["AllDone"] = !s.st.anyQuizLeft()
		if episodeID > 0 {
			ep, _ := s.st.Episode(episodeID)
			data["Episode"] = ep
		}
		s.render(w, r, "quiz", "Look Who's Talking Now", "feeds", data)
		return
	}
	ep, _ := s.st.Episode(q.Version.EpisodeID)
	data["Episode"] = ep
	data["Version"] = q.Version
	data["Window"] = win
	data["Key"] = win.key(q.Version.ID)
	total, done := quizProgress(q.Windows, q.Checked)
	data["EpisodePercent"] = percent(done, total)
	// audio: just this passage (media fragment - the browser stops at the end)
	from, to := max(0, win.StartMs-400), win.EndMs+600
	data["Audio"] = fmt.Sprintf("/audio/%s#t=%.1f,%.1f", q.Version.AudioFile, float64(from)/1000, float64(to)/1000)
	data["AudioFrom"], data["AudioTo"] = from, to

	// chips: hosts and regulars of the podcast, then per row its current
	// speaker if that is someone else; everybody else in "Someone else"
	chipPs, otherPs := s.st.chipPeople(feedID, q.Version.EpisodeID)
	var chips, others []quizChoice
	for _, p := range chipPs {
		chips = append(chips, quizChoice{Value: fmt.Sprintf("p%d", p.ID), Name: p.Name, Class: spClass(p.Label)})
	}
	for _, p := range otherPs {
		others = append(others, quizChoice{Value: fmt.Sprintf("p%d", p.ID), Name: p.Name, Class: spClass(p.Label)})
	}
	var rows []quizRowView
	for i, row := range win.Rows {
		cur := labelValue(row.Label)
		v := quizRowView{I: i, StartMs: row.StartMs, EndMs: row.EndMs, Current: cur,
			CurrentName: labelName(row.Label), Class: spClass(row.Label), Words: row.Words,
			Offset: clockShort(row.StartMs - win.StartMs)}
		found := false
		for _, c := range chips {
			if c.Value == cur {
				found = true
			}
		}
		if !found && cur != "x" && cur != "u" {
			name := labelName(row.Label)
			if _, isPerson := labelPerson(row.Label); !isPerson {
				name = labelName(row.Label) + " (no name yet)"
			}
			v.Choices = append(v.Choices, quizChoice{Value: cur, Name: name, Class: spClass(row.Label)})
		}
		v.Choices = append(v.Choices, chips...)
		v.Choices = append(v.Choices, quizChoice{"x", "Several at once", "spx"}, quizChoice{"u", "Nobody / music", "spu"})
		rows = append(rows, v)
	}
	data["Rows"] = rows
	data["Others"] = others
	s.render(w, r, "quiz", "Look Who's Talking Now", "feeds", data)
}

func clockShort(ms int64) string {
	sec := max(ms, 0) / 1000
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func percent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	p := int(done * 100 / total)
	if p == 100 && done < total {
		p = 99
	}
	return p
}

// handleQuizAnswer stores the answers of one passage and moves on.
func (s *Server) handleQuizAnswer(w http.ResponseWriter, r *http.Request) {
	v, ok := s.correctionVersion(w, r)
	if !ok {
		return
	}
	next := r.FormValue("next")
	if !(strings.HasPrefix(next, "/feeds/") || strings.HasPrefix(next, "/episodes/")) || !strings.HasSuffix(next, "/quiz") {
		next = fmt.Sprintf("/episodes/%d/quiz", v.EpisodeID)
	}
	ws, _ := strconv.ParseInt(r.FormValue("ws"), 10, 64)
	we, _ := strconv.ParseInt(r.FormValue("we"), 10, 64)
	n, _ := strconv.Atoi(r.FormValue("n"))
	if we <= ws || n <= 0 || n > 200 {
		back(w, r, next, "", "That passage could not be saved - please try the next one.")
		return
	}
	type answer struct {
		a, b     int64
		orig, to string
	}
	var ans []answer
	for i := 0; i < n; i++ {
		a, _ := strconv.ParseInt(r.FormValue(fmt.Sprintf("s%d", i)), 10, 64)
		b, _ := strconv.ParseInt(r.FormValue(fmt.Sprintf("e%d", i)), 10, 64)
		orig := r.FormValue(fmt.Sprintf("o%d", i))
		to := r.FormValue(fmt.Sprintf("l%d", i))
		if other := r.FormValue(fmt.Sprintf("x%d", i)); other != "" {
			to = other
		}
		if b <= a || a < ws-1000 || b > we+1000 {
			continue
		}
		if to == "" {
			to = orig
		}
		ans = append(ans, answer{a, b, orig, to})
	}
	_, _, turns, err := s.st.LoadResults(v.ID)
	if err != nil {
		back(w, r, next, "", err.Error())
		return
	}
	corr, _ := s.st.Corrections(v.ID)
	newLabel := -1
	resolve := func(val string) (int, int64, error) {
		switch {
		case val == "x":
			return labelCrosstalk, 0, nil
		case val == "u":
			return labelUnknown, 0, nil
		case val == "new":
			if newLabel < 0 {
				newLabel = nextLabel(turns, corr)
			}
			return newLabel, 0, nil
		case strings.HasPrefix(val, "p"):
			id, err := strconv.ParseInt(val[1:], 10, 64)
			if err != nil || id <= 0 {
				return 0, 0, fmt.Errorf("unknown speaker %q", val)
			}
			return personLabel(id), id, nil
		}
		l, err := strconv.Atoi(val)
		if err != nil || l < 0 {
			return 0, 0, fmt.Errorf("unknown speaker %q", val)
		}
		return l, 0, nil
	}
	changed := 0
	uid := s.userID(r)
	for i := 0; i < len(ans); {
		if ans[i].to == ans[i].orig {
			i++
			continue
		}
		j := i // neighbouring rows with the same new speaker become one correction
		for j+1 < len(ans) && ans[j+1].to == ans[i].to && ans[j+1].to != ans[j+1].orig {
			j++
		}
		label, personID, err := resolve(ans[i].to)
		if err != nil {
			back(w, r, next, "", err.Error())
			return
		}
		c := Correction{Kind: "range", StartMs: ans[i].a, EndMs: ans[j].b, Label: label, UserID: uid}
		cid, err := s.st.AddCorrectionID(v.ID, c)
		if err != nil {
			back(w, r, next, "", err.Error())
			return
		}
		if personID > 0 {
			if err := addSampleFromRange(r.Context(), s.st, v, personID, cid, c.StartMs, c.EndMs); err != nil {
				logf("warning: could not save voice sample: %v", err)
			}
		}
		changed += j - i + 1
		i = j + 1
	}
	if _, err := s.st.db.Exec(`INSERT INTO checks(version_id,start_ms,end_ms,user_id,changed,created_at) VALUES(?,?,?,?,?,?)`,
		v.ID, ws, we, uid, changed, time.Now().Unix()); err != nil {
		back(w, r, next, "", err.Error())
		return
	}
	quizRelease(fmt.Sprintf("%d:%d", v.ID, ws))
	s.st.loadQuiz(v.ID) // keeps the progress numbers current
	msg := "Thanks – that passage is checked."
	if changed > 0 {
		msg = fmt.Sprintf("Thanks – %d row%s corrected. Look who's talking now!", changed, map[bool]string{true: "", false: "s"}[changed == 1])
	}
	back(w, r, next, msg, "")
}

// ---------------------------------------------------------------- numbers

type quizFeedProgress struct {
	Episodes int // transcribed episodes
	Started  int // episodes with at least one checked passage
	Finished int
	Percent  int // of the passages of opened episodes
	Checks   int
	Helpers  int
}

func (s *Store) feedQuizProgress(feedID int64) quizFeedProgress {
	var p quizFeedProgress
	var total, done sql.NullInt64
	s.db.QueryRow(`SELECT COUNT(*),
		SUM(CASE WHEN v.quiz_done_ms>0 THEN 1 ELSE 0 END),
		SUM(CASE WHEN v.quiz_total_ms>0 AND v.quiz_done_ms>=v.quiz_total_ms THEN 1 ELSE 0 END),
		SUM(v.quiz_total_ms), SUM(v.quiz_done_ms)
		FROM episodes e JOIN versions v ON v.id=e.active_version_id
		WHERE e.feed_id=? AND e.status='done'`, feedID).Scan(&p.Episodes, &p.Started, &p.Finished, &total, &done)
	p.Percent = percent(done.Int64, total.Int64)
	s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT c.user_id) FROM checks c JOIN versions v ON v.id=c.version_id
		JOIN episodes e ON e.id=v.episode_id WHERE e.feed_id=? AND c.carried=0`, feedID).Scan(&p.Checks, &p.Helpers)
	return p
}

// anyQuizLeft: is there anything to check in any podcast? (episodes not
// measured yet count as "something left")
func (s *Store) anyQuizLeft() bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM episodes e JOIN versions v ON v.id=e.active_version_id
		WHERE e.status='done' AND v.audio_file!='' AND (v.quiz_total_ms=0 OR v.quiz_done_ms<v.quiz_total_ms) LIMIT 1)`).Scan(&n)
	return n > 0
}

// checksBy: passages checked by a user (0 = local).
func (s *Store) checksBy(userID int64) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM checks WHERE user_id=? AND carried=0`, userID).Scan(&n)
	return n
}

// checkState of a paragraph on the episode page: "checked", "part-checked" or "".
func checkState(ivs []interval, a, b int64) string {
	span := b - a
	if span <= 0 {
		return ""
	}
	c := float64(covered(ivs, a, b)) / float64(span)
	switch {
	case c >= quizCheckedPart:
		return "checked"
	case c > 0.2:
		return "part-checked"
	}
	return ""
}

// checkedLane: where someone confirmed the speakers, for the lanes above the transcript.
func checkedLane(ivs []interval, us []Utterance, totalMs int64) *markLane {
	if totalMs <= 0 && len(us) > 0 {
		totalMs = us[len(us)-1].EndMs
	}
	if totalMs <= 0 {
		return nil
	}
	l := &markLane{Mark: "checked", Name: "Checked"}
	for _, iv := range ivs {
		a, b := max(iv.a, 0), min(iv.b, totalMs)
		if b <= a {
			continue
		}
		l.TotalMs += b - a
		l.Blocks = append(l.Blocks, laneBlock{Left: float64(a) * 100 / float64(totalMs), Width: max(float64(b-a)*100/float64(totalMs), 0.15)})
	}
	return l
}

// carryChecks copies the checked passages of an old version to a new version
// of the same audio - but only passages whose speakers could be carried
// (named people, several at once, nobody). Passages with unnamed voices must
// be checked again: nobody confirmed who that voice is in the new version.
func carryChecks(st *Store, from, to Version, shown []Utterance) (int, error) {
	rows, err := st.db.Query(`SELECT start_ms, end_ms, user_id, changed, created_at FROM checks WHERE version_id=?`, from.ID)
	if err != nil {
		return 0, err
	}
	type chk struct {
		a, b, user, at int64
		changed        int
	}
	var cs []chk
	for rows.Next() {
		var c chk
		rows.Scan(&c.a, &c.b, &c.user, &c.changed, &c.at)
		cs = append(cs, c)
	}
	rows.Close()
	n := 0
	for _, c := range cs {
		// keep the parts spoken by carryable speakers; unnamed voices cut holes
		var parts []interval
		cur := interval{c.a, c.a}
		for _, u := range shown {
			if u.EndMs <= c.a || u.StartMs >= c.b || u.Mark != "" {
				continue
			}
			if carryable(u.Label) {
				continue
			}
			if hole := max(u.StartMs, c.a); hole > cur.a {
				parts = append(parts, interval{cur.a, hole})
			}
			cur.a = min(u.EndMs, c.b)
		}
		if c.b > cur.a {
			parts = append(parts, interval{cur.a, c.b})
		}
		for _, iv := range parts {
			if iv.b-iv.a < 1000 {
				continue
			}
			if _, err := st.db.Exec(`INSERT INTO checks(version_id,start_ms,end_ms,user_id,changed,created_at,carried) VALUES(?,?,?,?,?,?,1)`,
				to.ID, iv.a, iv.b, c.user, c.changed, c.at); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func hasChecks(st *Store, versionID int64) bool {
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM checks WHERE version_id=?`, versionID).Scan(&n)
	return n > 0
}
