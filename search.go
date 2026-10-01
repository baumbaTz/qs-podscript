package main

// Full-text search over the main version of every transcribed episode.
//
// The index is derived data: rows of ~30 words with their start time, built
// from what the episode page shows (corrections, spelling fixes and names
// applied; ads left out; movie clips flagged). A background indexer keeps it
// up to date: every episode has a fingerprint (main version + its
// corrections + spelling fixes + people's names); when it changes, the
// episode is indexed again. Any POST kicks the indexer, and it looks again
// every few minutes anyway.
//
// With SQLite's FTS5 (build tag sqlite_fts5, set in build.sh) the search is
// a real word index with ranking; without it a plain table with LIKE is used
// (slower, same results apart from the order).

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	searchRowsPerEpisode = 1_000_000 // rowid = episode*this + n (fast delete by range)
	searchChunkWords     = 30
	searchPageSize       = 30
)

var search struct {
	mu    sync.Mutex
	fts   bool // FTS5 available
	ready bool
	kick  chan struct{}
	// progress of the first build (shown on the search page)
	todo, done int
}

func init() { search.kick = make(chan struct{}, 1) }

// searchKick: something may have changed - look soon.
func searchKick() {
	select {
	case search.kick <- struct{}{}:
	default:
	}
}

// initSearch creates the tables (FTS5 if this build has it).
func initSearch(st *Store) error {
	engine := "plain"
	_, err := st.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(
		text, episode_id UNINDEXED, feed_id UNINDEXED, start_ms UNINDEXED, end_ms UNINDEXED,
		label UNINDEXED, mark UNINDEXED, tokenize='unicode61 remove_diacritics 2')`)
	if err == nil {
		engine = "fts5"
	} else if !strings.Contains(err.Error(), "no such module") {
		return err
	} else if _, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS search_plain(
		id INTEGER PRIMARY KEY, text TEXT NOT NULL, norm TEXT NOT NULL, episode_id INTEGER NOT NULL,
		feed_id INTEGER NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL,
		label INTEGER NOT NULL, mark TEXT NOT NULL)`); err != nil {
		return err
	}
	// corrections of one version are looked up all the time (episode page,
	// search fingerprints) - the table had no index for that
	if _, err := st.db.Exec(`CREATE INDEX IF NOT EXISTS idx_corrections_version ON corrections(version_id)`); err != nil {
		return err
	}
	if _, err := st.db.Exec(`CREATE TABLE IF NOT EXISTS search_state(
		episode_id INTEGER PRIMARY KEY, fp TEXT NOT NULL)`); err != nil {
		return err
	}
	if st.Setting("search_engine", "") != engine { // other build: start over
		st.db.Exec(`DELETE FROM search_state`)
		st.SetSetting("search_engine", engine)
	}
	search.mu.Lock()
	search.fts = engine == "fts5"
	search.mu.Unlock()
	return nil
}

func searchTable() string {
	if search.fts {
		return "search_fts"
	}
	return "search_plain"
}

// runSearchIndexer keeps the index up to date until ctx ends.
func runSearchIndexer(ctx context.Context, st *Store) {
	if err := initSearch(st); err != nil {
		logf("Search is not available: %v", err)
		return
	}
	if !search.fts {
		debugf("search: FTS5 not in this build, using the plain table")
	}
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		if n, err := updateSearchIndex(ctx, st); err != nil {
			debugf("search index: %v", err)
		} else if n > 0 {
			debugf("search index: %d episode(s) updated", n)
		}
		search.mu.Lock()
		search.ready = true
		search.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-search.kick:
			// a few POSTs often come together (e.g. several corrections)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		case <-tick.C:
		}
	}
}

// globalSearchFP: what changes every episode's text at once (spelling fixes,
// people's names).
func globalSearchFP(st *Store) string {
	var a, b string
	st.db.QueryRow(`SELECT COUNT(*)||':'||COALESCE(MAX(id),0)||':'||COALESCE(SUM(length(correct)+length(variants)+feed_id),0) FROM spelling`).Scan(&a)
	st.db.QueryRow(`SELECT COUNT(*)||':'||COALESCE(group_concat(id||'='||name, ','),'') FROM people`).Scan(&b)
	h := sha1.Sum([]byte(a + "|" + b))
	return hex.EncodeToString(h[:8])
}

func updateSearchIndex(ctx context.Context, st *Store) (int, error) {
	global := globalSearchFP(st)
	rows, err := st.db.Query(`SELECT e.id, e.feed_id, COALESCE(e.active_version_id,0),
		(SELECT COUNT(*)||':'||COALESCE(MAX(id),0) FROM corrections c WHERE c.version_id=e.active_version_id),
		COALESCE(s.fp,'')
		FROM episodes e LEFT JOIN search_state s ON s.episode_id=e.id
		WHERE e.active_version_id IS NOT NULL OR s.episode_id IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	type job struct {
		ep, feed, ver int64
		fp            string
	}
	var jobs []job
	for rows.Next() {
		var j job
		var corr, old string
		if err := rows.Scan(&j.ep, &j.feed, &j.ver, &corr, &old); err != nil {
			rows.Close()
			return 0, err
		}
		if j.ver != 0 {
			j.fp = fmt.Sprintf("%d|%s|%s", j.ver, corr, global)
		}
		if j.fp != old {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	// episodes that are gone completely (podcast removed)
	st.db.Exec(`DELETE FROM search_state WHERE episode_id NOT IN (SELECT id FROM episodes)`)
	if search.fts {
		st.db.Exec(`DELETE FROM search_fts WHERE episode_id NOT IN (SELECT id FROM episodes)`)
	} else {
		st.db.Exec(`DELETE FROM search_plain WHERE episode_id NOT IN (SELECT id FROM episodes)`)
	}
	search.mu.Lock()
	search.todo, search.done = len(jobs), 0
	search.mu.Unlock()
	for i, j := range jobs {
		if ctx.Err() != nil {
			return i, nil
		}
		if err := indexEpisode(st, j.ep, j.feed, j.ver, j.fp); err != nil {
			debugf("search: episode %d: %v", j.ep, err)
		}
		search.mu.Lock()
		search.done = i + 1
		search.mu.Unlock()
	}
	return len(jobs), nil
}

type searchChunk struct {
	text       string
	start, end int64
	label      int
	mark       string
}

// searchChunks splits what the episode page shows into rows of about
// searchChunkWords words, preferably at a sentence end.
func searchChunks(us []Utterance) []searchChunk {
	var out []searchChunk
	for _, u := range us {
		if u.Mark == markAd {
			continue
		}
		var cur []Word
		flush := func() {
			if len(cur) == 0 {
				return
			}
			parts := make([]string, len(cur))
			for i, w := range cur {
				parts[i] = w.Text
			}
			out = append(out, searchChunk{strings.Join(parts, " "), cur[0].StartMs, cur[len(cur)-1].EndMs, u.Label, u.Mark})
			cur = nil
		}
		for _, w := range u.Words {
			cur = append(cur, w)
			end := strings.TrimRight(w.Text, `"'”’)`)
			sentence := strings.HasSuffix(end, ".") || strings.HasSuffix(end, "?") || strings.HasSuffix(end, "!")
			if len(cur) >= searchChunkWords*2 || (len(cur) >= searchChunkWords && sentence) {
				flush()
			}
		}
		flush()
	}
	return out
}

func indexEpisode(st *Store, epID, feedID, verID int64, fp string) error {
	var chunks []searchChunk
	if verID != 0 {
		segs, toks, turns, err := st.LoadResults(verID)
		if err != nil {
			return err
		}
		corr, _ := st.Corrections(verID)
		chunks = searchChunks(applySpelling(buildUtterances(segs, toks, turns, corr), spellingFor(st, feedID)))
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lo := epID * searchRowsPerEpisode
	if _, err := tx.Exec(`DELETE FROM `+searchTable()+` WHERE rowid BETWEEN ? AND ?`, lo, lo+searchRowsPerEpisode-1); err != nil {
		return err
	}
	for i, c := range chunks {
		if i >= searchRowsPerEpisode {
			break
		}
		if search.fts {
			_, err = tx.Exec(`INSERT INTO search_fts(rowid,text,episode_id,feed_id,start_ms,end_ms,label,mark) VALUES(?,?,?,?,?,?,?,?)`,
				lo+int64(i), c.text, epID, feedID, c.start, c.end, c.label, c.mark)
		} else {
			_, err = tx.Exec(`INSERT INTO search_plain(id,text,norm,episode_id,feed_id,start_ms,end_ms,label,mark) VALUES(?,?,?,?,?,?,?,?,?)`,
				lo+int64(i), c.text, " "+searchNorm(c.text)+" ", epID, feedID, c.start, c.end, c.label, c.mark)
		}
		if err != nil {
			return err
		}
	}
	if fp == "" {
		_, err = tx.Exec(`DELETE FROM search_state WHERE episode_id=?`, epID)
	} else {
		_, err = tx.Exec(`INSERT INTO search_state(episode_id,fp) VALUES(?,?) ON CONFLICT(episode_id) DO UPDATE SET fp=excluded.fp`, epID, fp)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// searchNorm: lower case, accents removed, words separated by single spaces.
func searchNorm(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(foldAccents(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space { // also ' : "don't" = "don t", like the FTS5 word index
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var accentFold = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "Ä", "A", "Ö", "O", "Ü", "U", "ß", "ss",
	"é", "e", "è", "e", "ê", "e", "ë", "e", "á", "a", "à", "a", "â", "a", "í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ú", "u", "ù", "u", "û", "u", "ñ", "n", "ç", "c", "É", "E", "Á", "A", "Ó", "O")

func foldAccents(s string) string { return accentFold.Replace(s) }

// ---------------------------------------------------------------- query

// searchTerm: one word or "a phrase"; Prefix = written with a * at the end.
type searchTerm struct {
	Words  []string
	Prefix bool
}

// parseSearch: words (all must appear), "exact phrases", word* for words
// starting like that. Everything else is ignored, so no input can break the
// query syntax.
func parseSearch(q string) []searchTerm {
	var out []searchTerm
	q = strings.ReplaceAll(strings.ReplaceAll(q, "„", `"`), "“", `"`)
	q = strings.ReplaceAll(q, "”", `"`)
	parts := strings.Split(q, `"`)
	for i, p := range parts {
		if i%2 == 1 { // inside quotes
			if ws := strings.Fields(searchNorm(p)); len(ws) > 0 {
				out = append(out, searchTerm{Words: ws})
			}
			continue
		}
		for _, f := range strings.Fields(p) {
			prefix := strings.HasSuffix(f, "*")
			ws := strings.Fields(searchNorm(f))
			if len(ws) == 0 {
				continue
			}
			// "don't-stop" -> phrase of its parts
			out = append(out, searchTerm{Words: ws, Prefix: prefix})
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func (t searchTerm) fts() string {
	s := `"` + strings.Join(t.Words, " ") + `"`
	if t.Prefix {
		s += "*"
	}
	return s
}

type searchHit struct {
	EpisodeID int64   `json:"episode_id"`
	FeedID    int64   `json:"feed_id"`
	Episode   string  `json:"episode"`
	Podcast   string  `json:"podcast"`
	Art       string  `json:"-"` // the podcast's artwork file
	PubDate   int64   `json:"pub_date"`
	StartMs   int64   `json:"start_ms"`
	Speaker   string  `json:"speaker"`
	SpClass   string  `json:"sp_class"`
	Mark      string  `json:"mark,omitempty"`
	Text      string  `json:"text"`  // found words between \x01 and \x02
	Score     float64 `json:"score"` // lower = better (FTS5 bm25); 0 without FTS5
}

func (h searchHit) Link(hl string) string {
	return fmt.Sprintf("/episodes/%d?hl=%s#t%d", h.EpisodeID, url.QueryEscape(hl), h.StartMs)
}

func (h searchHit) Snippet() template.HTML { return markSnippet(h.Text) }

type searchQuery struct {
	Q       string
	Feed    int64
	Person  int64
	NoClips bool
	Sort    string // "" relevance | "new" | "old"
	Page    int
}

// runSearch returns up to limit hits from offset on, and the total number
// of matching passages.
func runSearch(st *Store, sq searchQuery, limit, offset int) ([]searchHit, int, error) {
	terms := parseSearch(sq.Q)
	if len(terms) == 0 {
		return nil, 0, nil
	}
	var where []string
	var args []any
	tbl := searchTable()
	if search.fts {
		parts := make([]string, len(terms))
		for i, t := range terms {
			parts[i] = t.fts()
		}
		where = append(where, "search_fts MATCH ?")
		args = append(args, strings.Join(parts, " AND "))
	} else {
		for _, t := range terms {
			p := " " + strings.Join(t.Words, " ")
			if !t.Prefix {
				p += " "
			}
			where = append(where, "norm LIKE ? ESCAPE '\\'")
			args = append(args, "%"+likeEscape(p)+"%")
		}
	}
	c := tbl + "." // FTS5's snippet() wants the table's own name, so no alias
	if sq.Feed > 0 {
		where = append(where, c+"feed_id=?")
		args = append(args, sq.Feed)
	}
	if sq.Person > 0 {
		where = append(where, c+"label=?")
		args = append(args, personLabel(sq.Person))
	}
	if sq.NoClips {
		where = append(where, c+"mark<>?")
		args = append(args, markClip)
	}
	from := tbl + " JOIN episodes e ON e.id=" + c + "episode_id JOIN feeds f ON f.id=" + c + "feed_id"
	cond := strings.Join(where, " AND ")
	var total int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM `+from+` WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "e.pub_date DESC, " + c + "start_ms"
	switch {
	case sq.Sort == "old":
		order = "e.pub_date ASC, " + c + "start_ms"
	case sq.Sort == "" && search.fts:
		order = "bm25(search_fts), e.pub_date DESC"
	}
	snip, score := c+"text", "0.0"
	if search.fts {
		snip, score = "snippet(search_fts, 0, char(1), char(2), '…', 24)", "bm25(search_fts)"
	}
	q := `SELECT ` + c + `episode_id, ` + c + `feed_id, e.title, f.title, f.image_file, e.pub_date, ` + c + `start_ms, ` + c + `label, ` + c + `mark, ` + snip + `, ` + score + `
		FROM ` + from + ` WHERE ` + cond + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	rows, err := st.db.Query(q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []searchHit
	for rows.Next() {
		var h searchHit
		var label int
		if err := rows.Scan(&h.EpisodeID, &h.FeedID, &h.Episode, &h.Podcast, &h.Art, &h.PubDate, &h.StartMs, &label, &h.Mark, &h.Text, &h.Score); err != nil {
			return nil, 0, err
		}
		if !search.fts {
			h.Text = markPlain(h.Text, terms)
		}
		h.Speaker, h.SpClass = labelName(label), spClass(label)
		out = append(out, h)
	}
	return out, total, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// markSnippet: FTS5 marks hits with \x01 … \x02 - escape the text, then <mark>.
func markSnippet(s string) template.HTML {
	s = html.EscapeString(s)
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\x01", "<mark>"), "\x02", "</mark>")
	return template.HTML(s)
}

// markPlain (no FTS5): put the FTS5 markers around the words of the terms.
func markPlain(text string, terms []searchTerm) string {
	words := strings.Fields(text)
	for i, w := range words {
		if wordHit(w, terms) {
			words[i] = "\x01" + w + "\x02"
		}
	}
	return strings.Join(words, " ")
}

// wordHit: does this transcript word belong to one of the terms?
func wordHit(w string, terms []searchTerm) bool {
	n := searchNorm(w)
	if n == "" {
		return false
	}
	for _, t := range terms {
		if strings.Contains(" "+n+" ", " "+strings.Join(t.Words, " ")+" ") {
			return true
		}
		for i, tw := range t.Words {
			last := i == len(t.Words)-1
			for _, nw := range strings.Fields(n) {
				if nw == tw || (last && t.Prefix && strings.HasPrefix(nw, tw)) {
					return true
				}
			}
		}
	}
	return false
}

// ---------------------------------------------------------------- page

// searchKicker: after every change (any POST) the index looks for work.
func searchKicker(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Method == http.MethodPost && !strings.HasSuffix(r.URL.Path, "/progress") { // helpers report progress often
			searchKick()
			if strings.HasPrefix(r.URL.Path, "/feeds") {
				artworkKick() // a podcast added, or its feeds changed
			}
		}
	})
}

// searchOpt: one entry of the podcast / person menus (value: the id; the
// API also takes "s12"/"l12" as written by 0.25.1).
type searchOpt struct {
	Value, Title string
}

type searchGroup struct {
	Label string // "" = no group heading (only one source)
	Opts  []searchOpt
}

// searchSel splits a menu value into source and id.
func searchSel(v string) (server bool, id int64) {
	if strings.HasPrefix(v, "s") || strings.HasPrefix(v, "l") {
		server = v[0] == 's'
		v = v[1:]
	}
	id, _ = strconv.ParseInt(v, 10, 64)
	if id <= 0 {
		return false, 0
	}
	return server, id
}

// apiSearchAnswer: GET /api/v1/search on the server.
type apiSearchAnswer struct {
	Hits     []searchHit `json:"hits"`
	Total    int         `json:"total"`
	Podcasts []struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
	} `json:"podcasts"`
	People []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"people"`
	Indexing string `json:"indexing,omitempty"`
}

const searchMaxLimit = 300 // most hits one API call returns

func parseSearchQuery(r *http.Request) (searchQuery, string, string) {
	q := r.URL.Query()
	sq := searchQuery{Q: strings.TrimSpace(q.Get("q")), Sort: q.Get("sort"), NoClips: q.Get("clips") == "0"}
	if len(sq.Q) > 200 {
		sq.Q = sq.Q[:200]
	}
	sq.Page, _ = strconv.Atoi(q.Get("page"))
	if sq.Page < 1 {
		sq.Page = 1
	}
	if sq.Sort != "new" && sq.Sort != "old" {
		sq.Sort = ""
	}
	return sq, q.Get("podcast"), q.Get("person")
}

func searchIndexing() string {
	search.mu.Lock()
	defer search.mu.Unlock()
	if search.ready {
		return ""
	}
	if search.todo > 0 {
		return fmt.Sprintf("%d of %d episodes", search.done, search.todo)
	}
	return "starting"
}

// handleAPISearch: the server's search for connected QS-PodScripts (and
// anybody else - it's the same as the public page).
func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	sq, pod, person := parseSearchQuery(r)
	_, sq.Feed = searchSel(pod)
	_, sq.Person = searchSel(person)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > searchMaxLimit {
		limit = searchPageSize
	}
	var out apiSearchAnswer
	out.Indexing = searchIndexing()
	if feeds, err := s.st.Feeds(); err == nil {
		for _, f := range feeds {
			out.Podcasts = append(out.Podcasts, struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			}{f.ID, f.Title})
		}
	}
	if people, err := s.st.People(); err == nil {
		for _, p := range people {
			out.People = append(out.People, struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			}{p.ID, p.Name})
		}
	}
	if sq.Q != "" {
		hits, total, err := runSearch(s.st, sq, limit, (sq.Page-1)*limit)
		if err != nil {
			apiError(w, http.StatusBadRequest, "the search didn't work")
			return
		}
		out.Hits, out.Total = hits, total
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	sq, podSel, personSel := parseSearchQuery(r)
	_, sq.Feed = searchSel(podSel)
	_, sq.Person = searchSel(personSel)
	data := map[string]any{"Query": sq, "PodcastSel": podSel, "PersonSel": personSel}

	feeds, _ := s.st.Feeds()
	people, _ := s.st.People()
	var pods, persons searchGroup
	for _, f := range feeds {
		pods.Opts = append(pods.Opts, searchOpt{strconv.FormatInt(f.ID, 10), f.Title})
	}
	for _, p := range people {
		persons.Opts = append(persons.Opts, searchOpt{strconv.FormatInt(p.ID, 10), p.Name})
	}
	data["PodcastGroups"] = []searchGroup{pods}
	if len(persons.Opts) > 0 {
		data["PersonGroups"] = []searchGroup{persons}
	}
	if ix := searchIndexing(); ix != "" {
		data["Indexing"] = ix
	}
	if sq.Q != "" {
		hits, total, err := runSearch(s.st, sq, searchPageSize, (sq.Page-1)*searchPageSize)
		if err != nil {
			debugf("search %q: %v", sq.Q, err)
			data["Error"] = "The search didn't work – try other words."
		}
		data["Hits"], data["Total"] = hits, total
		shown := (sq.Page-1)*searchPageSize + len(hits)
		if shown < total {
			data["Next"] = searchURL(sq, podSel, personSel, sq.Page+1)
		}
		if sq.Page > 1 {
			data["Prev"] = searchURL(sq, podSel, personSel, sq.Page-1)
		}
		data["From"] = (sq.Page-1)*searchPageSize + 1
		data["To"] = shown
	}
	title := "Search"
	if sq.Q != "" {
		title = sq.Q + " – Search"
	}
	s.render(w, r, "search", title, "search", data)
}

func searchURL(sq searchQuery, pod, person string, page int) string {
	v := url.Values{}
	v.Set("q", sq.Q)
	if pod != "" {
		v.Set("podcast", pod)
	}
	if person != "" {
		v.Set("person", person)
	}
	if sq.NoClips {
		v.Set("clips", "0")
	}
	if sq.Sort != "" {
		v.Set("sort", sq.Sort)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return "/search?" + v.Encode()
}

// markSearchHits flags the words of an episode page that match ?hl= (coming
// from a search result).
func markSearchHits(us []Utterance, hl string) {
	terms := parseSearch(hl)
	if len(terms) == 0 {
		return
	}
	for i := range us {
		for j := range us[i].Words {
			if wordHit(us[i].Words[j].Text, terms) {
				us[i].Words[j].Hit = true
			}
		}
	}
}
