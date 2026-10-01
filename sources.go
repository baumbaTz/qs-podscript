package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

// Several feeds per podcast. A podcast is a row in `feeds` (settings, people,
// spelling fixes hang on it); its feeds are rows in `feed_sources` - the main
// feed it was added with plus any archive feeds. All episodes of a podcast
// share one list and one oldest-first queue. The same episode showing up in
// two feeds is stored once.

var errDuplicateFeed = errors.New("this feed address is already used by a podcast")

type FeedSource struct {
	ID          int64
	FeedID      int64
	URL         string
	Title       string
	LastRefresh int64
	LastError   string
	Episodes    int  // episodes that came from this feed
	Main        bool // the feed the podcast was added with
}

func (s *Store) SourceExists(u string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM feed_sources WHERE url=?`, u).Scan(&n)
	if n == 0 {
		s.db.QueryRow(`SELECT COUNT(*) FROM feeds WHERE url=?`, u).Scan(&n)
	}
	return n > 0
}

func (s *Store) Sources(feedID int64) ([]FeedSource, error) {
	rows, err := s.db.Query(`SELECT s.id, s.feed_id, s.url, s.title, s.last_refresh, s.last_error,
		(SELECT COUNT(*) FROM episodes e WHERE e.source_id=s.id), s.url = f.url
		FROM feed_sources s JOIN feeds f ON f.id=s.feed_id
		WHERE s.feed_id=? ORDER BY (s.url = f.url) DESC, s.id`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedSource
	for rows.Next() {
		var fs FeedSource
		if err := rows.Scan(&fs.ID, &fs.FeedID, &fs.URL, &fs.Title, &fs.LastRefresh, &fs.LastError, &fs.Episodes, &fs.Main); err != nil {
			return nil, err
		}
		out = append(out, fs)
	}
	return out, rows.Err()
}

func (s *Store) AddSource(feedID int64, u, title string) (int64, error) {
	if s.SourceExists(u) {
		return 0, errDuplicateFeed
	}
	res, err := s.db.Exec(`INSERT INTO feed_sources(feed_id,url,title,created_at) VALUES(?,?,?,?)`,
		feedID, u, title, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) setSourceResult(id int64, title string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
		s.db.Exec(`UPDATE feed_sources SET last_error=? WHERE id=?`, msg, id)
		return
	}
	s.db.Exec(`UPDATE feed_sources SET title=?, last_refresh=?, last_error='' WHERE id=?`, title, time.Now().Unix(), id)
}

// RemoveSource removes one feed of a podcast (never the last one). Episodes
// that came from it and were never transcribed are removed too; transcribed
// ones stay. If it was the main feed, another feed becomes the main one.
func (s *Store) RemoveSource(feedID, sourceID int64) (removedEpisodes int64, err error) {
	srcs, err := s.Sources(feedID)
	if err != nil {
		return 0, err
	}
	var gone *FeedSource
	for i := range srcs {
		if srcs[i].ID == sourceID {
			gone = &srcs[i]
		}
	}
	if gone == nil {
		return 0, fmt.Errorf("feed not found")
	}
	if len(srcs) < 2 {
		return 0, fmt.Errorf("a podcast needs at least one feed")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM episodes WHERE source_id=? AND active_version_id IS NULL
		AND NOT EXISTS (SELECT 1 FROM versions v WHERE v.episode_id=episodes.id)`, sourceID)
	if err != nil {
		return 0, err
	}
	removedEpisodes, _ = res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM feed_sources WHERE id=?`, sourceID); err != nil {
		return 0, err
	}
	if gone.Main {
		for _, o := range srcs {
			if o.ID != sourceID {
				if _, err := tx.Exec(`UPDATE feeds SET url=? WHERE id=?`, o.URL, feedID); err != nil {
					return 0, err
				}
				break
			}
		}
	}
	return removedEpisodes, tx.Commit()
}

// audioKey: the enclosure file name without query ("…/ABC123.mp3?updated=…"
// -> "abc123.mp3"), to recognise the same episode in two feeds when the guids
// differ. Very short or generic names don't count.
func audioKey(u string) string {
	pu, err := url.Parse(u)
	if err != nil {
		return ""
	}
	b := strings.ToLower(path.Base(pu.Path))
	if len(b) < 8 || b == "audio.mp3" || b == "episode.mp3" || b == "media.mp3" || b == "download.mp3" {
		return ""
	}
	return b
}

func titleKey(t string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(t) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// UpsertEpisodes adds a feed's items to its podcast. An item is the same
// episode as a known one when the guid matches, or - across feeds - when the
// audio file name matches, or the title matches and it was published on the
// same day. Only the feed that brought an episode updates its details, and
// only from the item with the same guid.
// Status and versions of known episodes are never touched. Returns new ones.
func (s *Store) UpsertEpisodes(feedID, sourceID int64, items []FeedItem) (int, error) {
	type known struct {
		id, source int64
		day        int64
	}
	byGUID := map[string]known{}
	byAudio := map[string]known{}
	byTitle := map[string]known{}
	rows, err := s.db.Query(`SELECT id, guid, audio_url, title, pub_date, source_id FROM episodes WHERE feed_id=?`, feedID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var k known
		var guid, au, title string
		var pub int64
		if err := rows.Scan(&k.id, &guid, &au, &title, &pub, &k.source); err != nil {
			rows.Close()
			return 0, err
		}
		k.day = pub / 86400
		byGUID[guid] = k
		if a := audioKey(au); a != "" {
			byAudio[a] = k
		}
		if t := titleKey(title); t != "" {
			byTitle[t] = k
		}
	}
	rows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	added := 0
	for _, it := range items {
		k, ok := byGUID[it.GUID]
		sameGUID := ok
		if !ok {
			if a := audioKey(it.AudioURL); a != "" {
				k, ok = byAudio[a]
			}
		}
		if !ok {
			if t, found := byTitle[titleKey(it.Title)]; found && titleKey(it.Title) != "" && abs64(t.day-it.PubDate/86400) <= 1 {
				k, ok = t, true
			}
		}
		if ok {
			// only the feed that brought it, and only for the very same item
			// (a look-alike found by file name or title keeps the first one)
			if sameGUID && (k.source == sourceID || k.source == 0) {
				if _, err := tx.Exec(`UPDATE episodes SET title=?, pub_date=?, audio_url=?, duration_s=?, source_id=? WHERE id=?`,
					it.Title, it.PubDate, it.AudioURL, it.DurationS, sourceID, k.id); err != nil {
					return 0, err
				}
			}
			continue
		}
		res, err := tx.Exec(`INSERT INTO episodes(feed_id,guid,title,pub_date,audio_url,duration_s,source_id)
			VALUES(?,?,?,?,?,?,?)`, feedID, it.GUID, it.Title, it.PubDate, it.AudioURL, it.DurationS, sourceID)
		if err != nil {
			return 0, err
		}
		id, _ := res.LastInsertId()
		k = known{id: id, source: sourceID, day: it.PubDate / 86400}
		byGUID[it.GUID] = k
		if a := audioKey(it.AudioURL); a != "" {
			byAudio[a] = k
		}
		if t := titleKey(it.Title); t != "" {
			byTitle[t] = k
		}
		added++
	}
	return added, tx.Commit()
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// refreshPodcast reads all feeds of a podcast. The podcast's title comes from
// its main feed. Feeds that fail are reported but don't stop the others.
func refreshPodcast(ctx context.Context, st *Store, f Feed) (added int, problems []string) {
	srcs, err := st.Sources(f.ID)
	if err != nil {
		return 0, []string{err.Error()}
	}
	for _, src := range srcs {
		if ctx.Err() != nil {
			break
		}
		title, items, err := fetchFeed(ctx, src.URL)
		st.setSourceResult(src.ID, title, err)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", src.URL, err))
			logf("Podcast %d (%s), feed %s: %v", f.ID, f.Title, src.URL, err)
			continue
		}
		n, err := st.UpsertEpisodes(f.ID, src.ID, items)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		added += n
		if src.Main {
			st.UpdateFeedMeta(f.ID, title)
		}
		logf("Podcast %d (%s), feed %q: %d episodes, %d new", f.ID, f.Title, title, len(items), n)
	}
	return added, problems
}
