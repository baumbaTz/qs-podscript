package main

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Design rule: we store RAW results (whisper segments + tokens, diarization
// turns, per-cluster voice embeddings), never finished transcript text.
// The readable transcript is built at display time (see merge.go). That makes
// relabeling speakers instant and lets us re-run only diarization later.

const schemaVersion = 18

var schema = []string{
	`CREATE TABLE settings(
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL)`,
	`CREATE TABLE feeds(
		id            INTEGER PRIMARY KEY,
		url           TEXT NOT NULL UNIQUE,
		title         TEXT NOT NULL DEFAULT '',
		language      TEXT NOT NULL DEFAULT 'auto',
		num_speakers  INTEGER NOT NULL DEFAULT 0,
		speaker_names TEXT NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL,
		last_refresh  INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE episodes(
		id                INTEGER PRIMARY KEY,
		feed_id           INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		guid              TEXT NOT NULL,
		title             TEXT NOT NULL,
		pub_date          INTEGER NOT NULL,
		audio_url         TEXT NOT NULL,
		duration_s        INTEGER NOT NULL DEFAULT 0,
		status            TEXT NOT NULL DEFAULT 'new',
		error             TEXT NOT NULL DEFAULT '',
		priority          INTEGER NOT NULL DEFAULT 0,
		active_version_id INTEGER,
		UNIQUE(feed_id, guid))`,
	`CREATE INDEX idx_episodes_queue ON episodes(status, priority DESC, pub_date ASC)`,
	`CREATE TABLE versions(
		id              INTEGER PRIMARY KEY,
		episode_id      INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
		created_at      INTEGER NOT NULL,
		status          TEXT NOT NULL,
		whisper_model   TEXT NOT NULL,
		whisper_backend TEXT NOT NULL,
		language        TEXT NOT NULL,
		audio_seconds   REAL NOT NULL DEFAULT 0,
		audio_file      TEXT NOT NULL DEFAULT '',
		num_clusters    INTEGER NOT NULL DEFAULT 0,
		timing          TEXT NOT NULL DEFAULT '',
		error           TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE segments(
		version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
		idx        INTEGER NOT NULL,
		start_ms   INTEGER NOT NULL,
		end_ms     INTEGER NOT NULL,
		text       TEXT NOT NULL,
		PRIMARY KEY(version_id, idx))`,
	`CREATE TABLE tokens(
		version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
		seg_idx    INTEGER NOT NULL,
		idx        INTEGER NOT NULL,
		start_ms   INTEGER NOT NULL,
		end_ms     INTEGER NOT NULL,
		text       TEXT NOT NULL,
		p          REAL NOT NULL,
		PRIMARY KEY(version_id, seg_idx, idx))`,
	`CREATE TABLE turns(
		version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
		start_ms   INTEGER NOT NULL,
		end_ms     INTEGER NOT NULL,
		cluster    INTEGER NOT NULL)`,
	`CREATE INDEX idx_turns_version ON turns(version_id, start_ms)`,
	`CREATE TABLE cluster_embeddings(
		version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
		cluster    INTEGER NOT NULL,
		seconds    REAL NOT NULL,
		model      TEXT NOT NULL,
		vec        BLOB NOT NULL,
		PRIMARY KEY(version_id, cluster))`,
}

type Store struct{ db *sql.DB }

func openStore() (*Store, error) {
	// mattn/go-sqlite3 strips the ?params from a plain path DSN itself.
	// several connections: pages can read while something else writes (WAL);
	// write transactions take the lock at once so two writers wait for each
	// other instead of failing
	dsn := P.DB + "?_journal_mode=WAL&_busy_timeout=10000&_foreign_keys=on&_txlock=immediate&_synchronous=NORMAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("database migration: %w", err)
	}
	refreshPeopleCache(s)
	setSpeakerDevice(s.Setting("speaker_device", deviceAuto))
	return s, nil
}

func (s *Store) Close() { s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= schemaVersion {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if v == 0 {
		for _, q := range schema {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 5 {
		if err := migratePeople(tx); err != nil {
			return fmt.Errorf("people tables: %w", err)
		}
	}
	if v < 4 {
		// automatic voice merges are stored as corrections too (undoable)
		for _, q := range []string{
			`ALTER TABLE corrections ADD COLUMN auto INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE corrections ADD COLUMN score REAL NOT NULL DEFAULT 0`,
		} {
			if v < 3 {
				break // table is created below with the columns added afterwards
			}
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	if v < 3 {
		// manual speaker corrections, per version
		if _, err := tx.Exec(`CREATE TABLE corrections(
			id         INTEGER PRIMARY KEY,
			version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
			kind       TEXT NOT NULL,              -- 'range' or 'merge'
			start_ms   INTEGER NOT NULL DEFAULT 0, -- range
			end_ms     INTEGER NOT NULL DEFAULT 0, -- range
			from_label INTEGER NOT NULL DEFAULT 0, -- merge: this speaker ...
			label      INTEGER NOT NULL,           -- ... becomes / range gets this speaker
			created_at INTEGER NOT NULL,
			auto       INTEGER NOT NULL DEFAULT 0, -- 1 = automatic voice merge
			score      REAL NOT NULL DEFAULT 0)`); err != nil {
			return err
		}
	}
	if v < 2 {
		// speaker detection settings used for a version (for comparing versions)
		if _, err := tx.Exec(`ALTER TABLE versions ADD COLUMN diarize_info TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if v < 6 {
		// text corrections; per-podcast voice activity detection
		for _, q := range []string{
			`ALTER TABLE corrections ADD COLUMN text TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE feeds ADD COLUMN vad INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	if v < 7 {
		// where a correction came from: "" = the user, "carried" = copied from
		// the previous version when transcribing again
		if _, err := tx.Exec(`ALTER TABLE corrections ADD COLUMN origin TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if v < 8 {
		// spelling fixes shown in transcripts; feed_id 0 = all podcasts
		if _, err := tx.Exec(`CREATE TABLE spelling(
			id         INTEGER PRIMARY KEY,
			feed_id    INTEGER NOT NULL DEFAULT 0,
			correct    TEXT NOT NULL,
			variants   TEXT NOT NULL,   -- one per line
			created_at INTEGER NOT NULL)`); err != nil {
			return err
		}
	}
	if v < 9 {
		// a podcast (table feeds) can have several feeds: current + archives.
		// feeds.url stays the main feed; every feed incl. the main one is a
		// row in feed_sources; episodes remember which feed brought them.
		for _, q := range []string{
			`CREATE TABLE feed_sources(
				id           INTEGER PRIMARY KEY,
				feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
				url          TEXT NOT NULL UNIQUE,
				title        TEXT NOT NULL DEFAULT '',
				created_at   INTEGER NOT NULL,
				last_refresh INTEGER NOT NULL DEFAULT 0,
				last_error   TEXT NOT NULL DEFAULT '')`,
			`INSERT INTO feed_sources(feed_id, url, title, created_at, last_refresh)
				SELECT id, url, title, created_at, last_refresh FROM feeds`,
			`ALTER TABLE episodes ADD COLUMN source_id INTEGER NOT NULL DEFAULT 0`,
			`UPDATE episodes SET source_id = (SELECT s.id FROM feed_sources s WHERE s.feed_id = episodes.feed_id)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 10 {
		// server mode: users, their podcasts, login sessions
		for _, q := range authSchema {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 11 {
		// server mode: API tokens of connected apps, leases for episodes they transcribe
		for _, q := range remoteSchema {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 12 {
		// server mode: who did what
		for _, q := range activitySchema {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 13 {
		// public names (anonymous by default), computer name on reservations
		for _, q := range []string{
			`ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE users ADD COLUMN show_name INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE episodes ADD COLUMN lease_device TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE episodes ADD COLUMN lease_since INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 14 {
		// checking in small pieces ("Look Who's Talking"): passages confirmed by someone
		for _, q := range []string{
			`CREATE TABLE checks(
				id         INTEGER PRIMARY KEY,
				version_id INTEGER NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
				start_ms   INTEGER NOT NULL,
				end_ms     INTEGER NOT NULL,
				user_id    INTEGER NOT NULL DEFAULT 0,
				changed    INTEGER NOT NULL DEFAULT 0, -- rows whose speaker was changed
				created_at INTEGER NOT NULL)`,
			`CREATE INDEX idx_checks_version ON checks(version_id, start_ms)`,
			`CREATE INDEX idx_checks_user ON checks(user_id)`,
			// cached progress: length of all passages / of the checked ones
			`ALTER TABLE versions ADD COLUMN quiz_total_ms INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE versions ADD COLUMN quiz_done_ms INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 15 {
		// checks copied to a new version (not counted again as someone's work)
		if _, err := tx.Exec(`ALTER TABLE checks ADD COLUMN carried INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if v < 16 {
		// who is in an episode (optional): recognition only considers these people
		for _, q := range []string{
			`CREATE TABLE episode_people(
				episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
				person_id  INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
				PRIMARY KEY(episode_id, person_id))`,
			`ALTER TABLE episodes ADD COLUMN people_limited INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 17 {
		// server: work for helpers' computers other than a full transcription
		for _, q := range []string{
			`ALTER TABLE episodes ADD COLUMN job_kind TEXT NOT NULL DEFAULT ''`,     // '' | 'diarize'
			`ALTER TABLE episodes ADD COLUMN job_source INTEGER NOT NULL DEFAULT 0`, // version to start from
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if v < 18 {
		// podcast artwork (artwork.go): a small copy is kept in the data folder
		for _, q := range []string{
			`ALTER TABLE feeds ADD COLUMN image_url TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE feeds ADD COLUMN image_file TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE feeds ADD COLUMN image_checked INTEGER NOT NULL DEFAULT 0`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return fmt.Errorf("%w\n%s", err, q)
			}
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- settings

func (s *Store) Setting(key, def string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---------------------------------------------------------------- feeds

type Feed struct {
	ID           int64
	URL          string
	Title        string
	Language     string
	NumSpeakers  int
	SpeakerNames string
	LastRefresh  int64
	VAD          bool   // voice activity detection (skip music) - not for shows with songs
	Image        string // small copy of the podcast's artwork in P.Images ("" = none yet)
}

// ImageURL: where the page loads the artwork from ("" = none).
func (f Feed) ImageURL() string {
	if f.Image == "" {
		return ""
	}
	return "/img/" + f.Image
}

// AddFeed adds a podcast with its main feed.
func (s *Store) AddFeed(f Feed) (int64, error) {
	if s.SourceExists(f.URL) {
		return 0, errDuplicateFeed
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, err := tx.Exec(`INSERT INTO feeds(url,title,language,num_speakers,speaker_names,created_at)
		VALUES(?,?,?,?,?,?)`, f.URL, f.Title, f.Language, f.NumSpeakers, f.SpeakerNames, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO feed_sources(feed_id,url,title,created_at) VALUES(?,?,?,?)`, id, f.URL, f.Title, now); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) UpdateFeedMeta(id int64, title string) error {
	_, err := s.db.Exec(`UPDATE feeds SET title=?, last_refresh=? WHERE id=?`, title, time.Now().Unix(), id)
	return err
}

func (s *Store) UpdateFeedSettings(f Feed) error {
	_, err := s.db.Exec(`UPDATE feeds SET language=?, num_speakers=?, speaker_names=?, vad=? WHERE id=?`,
		f.Language, f.NumSpeakers, f.SpeakerNames, f.VAD, f.ID)
	return err
}

func (s *Store) Feeds() ([]Feed, error) {
	rows, err := s.db.Query(`SELECT id,url,title,language,num_speakers,speaker_names,last_refresh,vad,image_file FROM feeds ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Feed
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.ID, &f.URL, &f.Title, &f.Language, &f.NumSpeakers, &f.SpeakerNames, &f.LastRefresh, &f.VAD, &f.Image); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) Feed(id int64) (Feed, error) {
	var f Feed
	err := s.db.QueryRow(`SELECT id,url,title,language,num_speakers,speaker_names,last_refresh,vad,image_file FROM feeds WHERE id=?`, id).
		Scan(&f.ID, &f.URL, &f.Title, &f.Language, &f.NumSpeakers, &f.SpeakerNames, &f.LastRefresh, &f.VAD, &f.Image)
	if errors.Is(err, sql.ErrNoRows) {
		return f, fmt.Errorf("feed %d not found", id)
	}
	return f, err
}

// ---------------------------------------------------------------- episodes

type Episode struct {
	ID              int64
	FeedID          int64
	GUID            string
	Title           string
	PubDate         int64
	AudioURL        string
	DurationS       int
	Status          string
	Error           string
	Priority        int
	ActiveVersionID sql.NullInt64
	SourceID        int64 // which of the podcast's feeds brought it
}

const episodeCols = `id,feed_id,guid,title,pub_date,audio_url,duration_s,status,error,priority,active_version_id,source_id`

func scanEpisode(sc interface{ Scan(...any) error }) (Episode, error) {
	var e Episode
	err := sc.Scan(&e.ID, &e.FeedID, &e.GUID, &e.Title, &e.PubDate, &e.AudioURL, &e.DurationS,
		&e.Status, &e.Error, &e.Priority, &e.ActiveVersionID, &e.SourceID)
	return e, err
}

func (s *Store) Episode(id int64) (Episode, error) {
	e, err := scanEpisode(s.db.QueryRow(`SELECT `+episodeCols+` FROM episodes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, fmt.Errorf("episode %d not found", id)
	}
	return e, err
}

func (s *Store) Episodes(feedID int64, status string, limit int) ([]Episode, error) {
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE 1=1`
	var args []any
	if feedID > 0 {
		q += ` AND feed_id=?`
		args = append(args, feedID)
	}
	if status != "" {
		q += ` AND status=?`
		args = append(args, status)
	}
	q += ` ORDER BY pub_date ASC, id ASC`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Episode
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NextEpisode picks the next queue item: explicitly queued/prioritized first,
// then oldest episode first.
func (s *Store) NextEpisode(feedID int64, includeErrors bool) (Episode, bool, error) {
	// job_kind 'diarize' = only redo the speaker detection (processQueued)
	q := `SELECT ` + episodeCols + ` FROM episodes WHERE status IN ('new','queued'`
	if includeErrors {
		q += `,'error'`
	}
	q += `)`
	var args []any
	if feedID > 0 {
		q += ` AND feed_id=?`
		args = append(args, feedID)
	}
	q += ` ORDER BY priority DESC, pub_date ASC, id ASC LIMIT 1`
	e, err := scanEpisode(s.db.QueryRow(q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

// ClaimEpisode atomically marks an episode as processing, so two workers
// (e.g. the web UI and a CLI "run") never process the same episode.
func (s *Store) ClaimEpisode(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE episodes SET status='processing', error='' WHERE id=? AND status<>'processing'`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// QueueEpisode puts an episode at the front of the queue (also done ones: a
// new version will be created).
func (s *Store) QueueEpisode(id int64) error {
	// asked for by hand: ahead of everything that is waiting
	_, err := s.db.Exec(`UPDATE episodes SET status='queued', priority=`+topPriority+`, error='', job_kind='', job_source=0
		WHERE id=? AND status NOT IN ('processing','leased')`, id)
	return err
}

func (s *Store) SetActiveVersion(episodeID, versionID int64) error {
	res, err := s.db.Exec(`UPDATE episodes SET active_version_id=? WHERE id=?
		AND EXISTS(SELECT 1 FROM versions WHERE id=? AND episode_id=? AND status='done')`,
		versionID, episodeID, versionID, episodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("version %d is not a finished version of episode %d", versionID, episodeID)
	}
	return nil
}

func (s *Store) Version(id int64) (Version, error) {
	var v Version
	err := s.db.QueryRow(`SELECT id,episode_id,created_at,status,whisper_model,whisper_backend,language,
		audio_seconds,audio_file,num_clusters,timing,error,diarize_info,transcribed_by,transcribed_on FROM versions WHERE id=?`, id).
		Scan(&v.ID, &v.EpisodeID, &v.CreatedAt, &v.Status, &v.WhisperModel, &v.WhisperBackend,
			&v.Language, &v.AudioSeconds, &v.AudioFile, &v.NumClusters, &v.Timing, &v.Error, &v.DiarizeInfo, &v.TranscribedBy, &v.TranscribedOn)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("version %d not found", id)
	}
	return v, err
}

func (s *Store) SetEpisodeStatus(id int64, status, errMsg string) error {
	_, err := s.db.Exec(`UPDATE episodes SET status=?, error=? WHERE id=?`, status, errMsg, id)
	return err
}

func (s *Store) FinishEpisode(id, versionID int64) error {
	_, err := s.db.Exec(`UPDATE episodes SET status='done', error='', priority=0, active_version_id=?, job_kind='', job_source=0 WHERE id=?`, versionID, id)
	return err
}

// ResetStuck puts episodes left in 'processing' (crash/kill) back into the queue.
func (s *Store) ResetStuck() (int64, error) {
	// episodes that already had a finished version (e.g. interrupted while redoing
	// speaker detection) go back to 'done', the rest back into the queue
	res, err := s.db.Exec(`UPDATE episodes SET status = CASE WHEN active_version_id IS NULL THEN 'new' ELSE 'done' END
		WHERE status='processing'`)
	if err != nil {
		return 0, err
	}
	s.db.Exec(`UPDATE versions SET status='error', error='interrupted' WHERE status='running'`)
	return res.RowsAffected()
}

func (s *Store) StatusCounts(feedID int64) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM episodes WHERE feed_id=? GROUP BY status`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		m[st] = n
	}
	return m, rows.Err()
}

// ---------------------------------------------------------------- versions

type Version struct {
	ID             int64
	EpisodeID      int64
	CreatedAt      int64
	Status         string
	WhisperModel   string
	WhisperBackend string
	Language       string
	AudioSeconds   float64
	AudioFile      string
	NumClusters    int
	Timing         string
	Error          string
	DiarizeInfo    string
	TranscribedBy  int64  // server mode: user whose computer transcribed it (0 = here)
	TranscribedOn  string // that computer's name
}

func (s *Store) CreateVersion(v Version) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO versions(episode_id,created_at,status,whisper_model,whisper_backend,language,transcribed_by,transcribed_on)
		VALUES(?,?,?,?,?,?,?,?)`, v.EpisodeID, time.Now().Unix(), "running", v.WhisperModel, v.WhisperBackend, v.Language, v.TranscribedBy, v.TranscribedOn)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) FinishVersion(v Version) error {
	_, err := s.db.Exec(`UPDATE versions SET status=?, audio_seconds=?, audio_file=?, num_clusters=?, timing=?, error=?, diarize_info=? WHERE id=?`,
		v.Status, v.AudioSeconds, v.AudioFile, v.NumClusters, v.Timing, v.Error, v.DiarizeInfo, v.ID)
	return err
}

func (s *Store) DeleteVersion(id int64) error {
	_, err := s.db.Exec(`DELETE FROM versions WHERE id=?`, id)
	return err
}

func (s *Store) Versions(episodeID int64) ([]Version, error) {
	rows, err := s.db.Query(`SELECT id,episode_id,created_at,status,whisper_model,whisper_backend,language,
		audio_seconds,audio_file,num_clusters,timing,error,diarize_info,transcribed_by,transcribed_on FROM versions WHERE episode_id=? ORDER BY id`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.EpisodeID, &v.CreatedAt, &v.Status, &v.WhisperModel, &v.WhisperBackend,
			&v.Language, &v.AudioSeconds, &v.AudioFile, &v.NumClusters, &v.Timing, &v.Error, &v.DiarizeInfo, &v.TranscribedBy, &v.TranscribedOn); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- raw results

type Segment struct {
	Idx     int
	StartMs int64
	EndMs   int64
	Text    string
}

type Token struct {
	SegIdx  int
	Idx     int
	StartMs int64
	EndMs   int64
	Text    string
	P       float64
}

type Turn struct {
	StartMs int64
	EndMs   int64
	Cluster int
}

type ClusterEmbedding struct {
	Cluster int
	Seconds float64
	Model   string
	Vec     []float32
}

// CopyTranscription reuses the whisper results of one version for another
// (used when only speaker detection is redone).
func (s *Store) CopyTranscription(fromVersion, toVersion int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO segments(version_id,idx,start_ms,end_ms,text)
		SELECT ?,idx,start_ms,end_ms,text FROM segments WHERE version_id=?`, toVersion, fromVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tokens(version_id,seg_idx,idx,start_ms,end_ms,text,p)
		SELECT ?,seg_idx,idx,start_ms,end_ms,text,p FROM tokens WHERE version_id=?`, toVersion, fromVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveTranscription(versionID int64, segs []Segment, toks []Token) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ss, err := tx.Prepare(`INSERT INTO segments(version_id,idx,start_ms,end_ms,text) VALUES(?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ss.Close()
	for _, g := range segs {
		if _, err := ss.Exec(versionID, g.Idx, g.StartMs, g.EndMs, g.Text); err != nil {
			return err
		}
	}
	ts, err := tx.Prepare(`INSERT INTO tokens(version_id,seg_idx,idx,start_ms,end_ms,text,p) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ts.Close()
	for _, t := range toks {
		if _, err := ts.Exec(versionID, t.SegIdx, t.Idx, t.StartMs, t.EndMs, t.Text, t.P); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SaveDiarization(versionID int64, turns []Turn, embs []ClusterEmbedding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range turns {
		if _, err := tx.Exec(`INSERT INTO turns(version_id,start_ms,end_ms,cluster) VALUES(?,?,?,?)`,
			versionID, t.StartMs, t.EndMs, t.Cluster); err != nil {
			return err
		}
	}
	for _, e := range embs {
		if _, err := tx.Exec(`INSERT INTO cluster_embeddings(version_id,cluster,seconds,model,vec) VALUES(?,?,?,?,?)`,
			versionID, e.Cluster, e.Seconds, e.Model, floatsToBlob(e.Vec)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadResults(versionID int64) ([]Segment, []Token, []Turn, error) {
	var segs []Segment
	var toks []Token
	var turns []Turn

	rows, err := s.db.Query(`SELECT idx,start_ms,end_ms,text FROM segments WHERE version_id=? ORDER BY idx`, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var g Segment
		if err := rows.Scan(&g.Idx, &g.StartMs, &g.EndMs, &g.Text); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		segs = append(segs, g)
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT seg_idx,idx,start_ms,end_ms,text,p FROM tokens WHERE version_id=? ORDER BY seg_idx, idx`, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.SegIdx, &t.Idx, &t.StartMs, &t.EndMs, &t.Text, &t.P); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		toks = append(toks, t)
	}
	rows.Close()

	rows, err = s.db.Query(`SELECT start_ms,end_ms,cluster FROM turns WHERE version_id=? ORDER BY start_ms`, versionID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.StartMs, &t.EndMs, &t.Cluster); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		turns = append(turns, t)
	}
	rows.Close()
	return segs, toks, turns, nil
}

// ---------------------------------------------------------------- corrections

type Correction struct {
	ID        int64
	Kind      string // "range" | "merge" | "text" | "mark"
	StartMs   int64
	EndMs     int64
	From      int
	Label     int
	CreatedAt int64
	Auto      bool    // made by the automatic voice merge
	Score     float64 // similarity for automatic merges
	Text      string  // kind "text": the corrected words
	Origin    string  // "" = made by the user, "carried" = copied from the previous version
	UserID    int64   // server mode: who made it (0 = local / automatic)
}

func (s *Store) Corrections(versionID int64) ([]Correction, error) {
	rows, err := s.db.Query(`SELECT id,kind,start_ms,end_ms,from_label,label,created_at,auto,score,text,origin,user_id
		FROM corrections WHERE version_id=? ORDER BY auto DESC, id`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Correction
	for rows.Next() {
		var c Correction
		if err := rows.Scan(&c.ID, &c.Kind, &c.StartMs, &c.EndMs, &c.From, &c.Label, &c.CreatedAt, &c.Auto, &c.Score, &c.Text, &c.Origin, &c.UserID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddCorrection(versionID int64, c Correction) error {
	_, err := s.db.Exec(`INSERT INTO corrections(version_id,kind,start_ms,end_ms,from_label,label,created_at,auto,score,text,origin,user_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, versionID, c.Kind, c.StartMs, c.EndMs, c.From, c.Label, time.Now().Unix(), c.Auto, c.Score, c.Text, c.Origin, c.UserID)
	return err
}

func (s *Store) DeleteCarriedCorrections(versionID int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM corrections WHERE version_id=? AND origin=?`, versionID, originCarried)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) DeleteAutoCorrections(versionID int64) error {
	_, err := s.db.Exec(`DELETE FROM corrections WHERE version_id=? AND auto=1`, versionID)
	return err
}

func (s *Store) ClusterEmbeddings(versionID int64) ([]ClusterEmbedding, error) {
	rows, err := s.db.Query(`SELECT cluster,seconds,model,vec FROM cluster_embeddings WHERE version_id=? ORDER BY cluster`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterEmbedding
	for rows.Next() {
		var e ClusterEmbedding
		var b []byte
		if err := rows.Scan(&e.Cluster, &e.Seconds, &e.Model, &b); err != nil {
			return nil, err
		}
		e.Vec = blobToFloats(b)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCorrection(versionID, id int64) error {
	_, err := s.db.Exec(`DELETE FROM corrections WHERE id=? AND version_id=?`, id, versionID)
	return err
}

func floatsToBlob(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func blobToFloats(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
