package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// People are global (the same host appears on several podcasts). In
// transcripts a person is encoded as a label >= personLabelBase, so the
// existing merge/correction logic works unchanged:
//
//	label  0..n        detected voice ("Speaker n+1")
//	label  -1 / -2     unknown / crosstalk
//	label  >= 1000000  person (id = label - personLabelBase)
const personLabelBase = 1000000

func personLabel(id int64) int { return personLabelBase + int(id) }

func labelPerson(l int) (int64, bool) {
	if l >= personLabelBase {
		return int64(l - personLabelBase), true
	}
	return 0, false
}

type Person struct {
	ID        int64
	Name      string
	CreatedAt int64
	Samples   int
	Seconds   float64
	Feeds     []string // "Title (host)"
}

type VoiceSample struct {
	ID           int64
	PersonID     int64
	VersionID    int64
	CorrectionID int64
	StartMs      int64
	EndMs        int64
	Seconds      float64
	Source       string // "confirmed" (from a user's correction)
	Vec          []float32
	CreatedAt    int64
	EpisodeID    int64 // filled for display
	EpisodeTitle string
}

const (
	roleHost = "host"
	rolePool = "pool"
)

// ---------------------------------------------------------------- names cache
// labelName() is used everywhere (templates, text export); person names are
// cached here and refreshed whenever people change.

var (
	peopleMu    sync.RWMutex
	peopleNames = map[int64]string{}
)

func refreshPeopleCache(st *Store) {
	ps, err := st.People()
	if err != nil {
		return
	}
	m := map[int64]string{}
	for _, p := range ps {
		m[p.ID] = p.Name
	}
	peopleMu.Lock()
	peopleNames = m
	peopleMu.Unlock()
}

func personName(id int64) string {
	peopleMu.RLock()
	defer peopleMu.RUnlock()
	if n, ok := peopleNames[id]; ok {
		return n
	}
	return "Removed person"
}

// ---------------------------------------------------------------- store

var peopleSchema = []string{
	`CREATE TABLE people(
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
		created_at INTEGER NOT NULL)`,
	`CREATE TABLE feed_people(
		feed_id   INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		person_id INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		role      TEXT NOT NULL,
		PRIMARY KEY(feed_id, person_id))`,
	// samples survive deletion of the version they came from (the voiceprint
	// is what matters), so version_id has no foreign key
	`CREATE TABLE voice_samples(
		id            INTEGER PRIMARY KEY,
		person_id     INTEGER NOT NULL REFERENCES people(id) ON DELETE CASCADE,
		version_id    INTEGER NOT NULL DEFAULT 0,
		correction_id INTEGER NOT NULL DEFAULT 0,
		start_ms      INTEGER NOT NULL DEFAULT 0,
		end_ms        INTEGER NOT NULL DEFAULT 0,
		seconds       REAL NOT NULL,
		source        TEXT NOT NULL,
		model         TEXT NOT NULL,
		vec           BLOB NOT NULL,
		created_at    INTEGER NOT NULL)`,
	`CREATE INDEX idx_samples_person ON voice_samples(person_id)`,
}

// migratePeople creates the tables and turns the old free-text speaker names
// of feeds into people + host roster entries.
func migratePeople(tx *sql.Tx) error {
	for _, q := range peopleSchema {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT id, speaker_names FROM feeds WHERE speaker_names<>''`)
	if err != nil {
		return err
	}
	type fn struct {
		id    int64
		names string
	}
	var fns []fn
	for rows.Next() {
		var f fn
		rows.Scan(&f.id, &f.names)
		fns = append(fns, f)
	}
	rows.Close()
	for _, f := range fns {
		for _, n := range strings.Split(f.names, ",") {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			tx.Exec(`INSERT OR IGNORE INTO people(name,created_at) VALUES(?,?)`, n, time.Now().Unix())
			var pid int64
			if err := tx.QueryRow(`SELECT id FROM people WHERE name=?`, n).Scan(&pid); err == nil {
				tx.Exec(`INSERT OR IGNORE INTO feed_people(feed_id,person_id,role) VALUES(?,?,?)`, f.id, pid, roleHost)
			}
		}
	}
	return nil
}

func (s *Store) People() ([]Person, error) {
	rows, err := s.db.Query(`SELECT p.id, p.name, p.created_at,
		(SELECT COUNT(*) FROM voice_samples v WHERE v.person_id=p.id),
		(SELECT COALESCE(SUM(seconds),0) FROM voice_samples v WHERE v.person_id=p.id)
		FROM people p ORDER BY p.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedAt, &p.Samples, &p.Seconds); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// which podcasts
	r2, err := s.db.Query(`SELECT fp.person_id, f.title, fp.role FROM feed_people fp JOIN feeds f ON f.id=fp.feed_id ORDER BY f.title`)
	if err != nil {
		return out, nil
	}
	defer r2.Close()
	feeds := map[int64][]string{}
	for r2.Next() {
		var pid int64
		var title, role string
		r2.Scan(&pid, &title, &role)
		feeds[pid] = append(feeds[pid], fmt.Sprintf("%s (%s)", title, roleText(role)))
	}
	for i := range out {
		out[i].Feeds = feeds[out[i].ID]
	}
	return out, nil
}

func roleText(r string) string {
	if r == roleHost {
		return "host"
	}
	return "regular"
}

func (s *Store) Person(id int64) (Person, error) {
	ps, err := s.People()
	if err != nil {
		return Person{}, err
	}
	for _, p := range ps {
		if p.ID == id {
			return p, nil
		}
	}
	return Person{}, fmt.Errorf("person %d not found", id)
}

// AddPerson creates a person or returns the existing one with that name.
func (s *Store) AddPerson(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("name is empty")
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO people(name,created_at) VALUES(?,?)`, name, time.Now().Unix()); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM people WHERE name=?`, name).Scan(&id)
	return id, err
}

func (s *Store) RenamePerson(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	_, err := s.db.Exec(`UPDATE people SET name=? WHERE id=?`, name, id)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("there is already a person called %s", name)
	}
	return err
}

// PersonUsage: in how many passages / episodes a person is named by hand
// (automatic recognition not counted - it is simply redone without them).
func (s *Store) PersonUsage(id int64) (passages, episodes int) {
	s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT v.episode_id) FROM corrections c
		JOIN versions v ON v.id=c.version_id
		WHERE c.auto=0 AND c.kind IN ('range','merge') AND c.label=?`, personLabel(id)).Scan(&passages, &episodes)
	return
}

func (s *Store) DeletePerson(id int64) error {
	_, err := s.db.Exec(`DELETE FROM people WHERE id=?`, id)
	return err
}

// Roster returns person id -> role for a feed.
func (s *Store) Roster(feedID int64) (map[int64]string, error) {
	rows, err := s.db.Query(`SELECT person_id, role FROM feed_people WHERE feed_id=?`, feedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]string{}
	for rows.Next() {
		var id int64
		var role string
		rows.Scan(&id, &role)
		m[id] = role
	}
	return m, rows.Err()
}

// SetRole sets one person's role on a podcast ("" = not on its list).
func (s *Store) SetRole(feedID, personID int64, role string) error {
	if _, err := s.db.Exec(`DELETE FROM feed_people WHERE feed_id=? AND person_id=?`, feedID, personID); err != nil {
		return err
	}
	if role != roleHost && role != rolePool {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO feed_people(feed_id,person_id,role) VALUES(?,?,?)`, feedID, personID, role)
	return err
}

func (s *Store) SetRoster(feedID int64, roles map[int64]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM feed_people WHERE feed_id=?`, feedID); err != nil {
		return err
	}
	for pid, role := range roles {
		if role != roleHost && role != rolePool {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO feed_people(feed_id,person_id,role) VALUES(?,?,?)`, feedID, pid, role); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddVoiceSample(v VoiceSample) error {
	_, err := s.db.Exec(`INSERT INTO voice_samples(person_id,version_id,correction_id,start_ms,end_ms,seconds,source,model,vec,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, v.PersonID, v.VersionID, v.CorrectionID, v.StartMs, v.EndMs, v.Seconds, v.Source,
		embeddingFile, floatsToBlob(v.Vec), time.Now().Unix())
	return err
}

func (s *Store) DeleteVoiceSample(id int64) error {
	_, err := s.db.Exec(`DELETE FROM voice_samples WHERE id=?`, id)
	return err
}

func (s *Store) DeleteSamplesOfCorrection(correctionID int64) error {
	_, err := s.db.Exec(`DELETE FROM voice_samples WHERE correction_id=?`, correctionID)
	return err
}

// DeleteVoiceSamplesIn removes a version's voice samples that lie mostly
// (at least half) inside [a,b] - used when a passage is marked as a movie clip.
func (s *Store) DeleteVoiceSamplesIn(versionID, a, b int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM voice_samples WHERE version_id=? AND end_ms>start_ms
		AND 2*(MIN(end_ms,?)-MAX(start_ms,?)) >= end_ms-start_ms`, versionID, b, a)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// VoiceSamples returns samples of one person (personID > 0) or all people.
func (s *Store) VoiceSamples(personID int64) ([]VoiceSample, error) {
	q := `SELECT v.id, v.person_id, v.version_id, v.correction_id, v.start_ms, v.end_ms, v.seconds, v.source, v.vec, v.created_at,
		COALESCE(e.id,0), COALESCE(e.title,'')
		FROM voice_samples v
		LEFT JOIN versions ver ON ver.id=v.version_id
		LEFT JOIN episodes e ON e.id=ver.episode_id
		WHERE v.model=?`
	args := []any{embeddingFile}
	if personID > 0 {
		q += ` AND v.person_id=?`
		args = append(args, personID)
	}
	q += ` ORDER BY v.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VoiceSample
	for rows.Next() {
		var v VoiceSample
		var b []byte
		if err := rows.Scan(&v.ID, &v.PersonID, &v.VersionID, &v.CorrectionID, &v.StartMs, &v.EndMs, &v.Seconds,
			&v.Source, &b, &v.CreatedAt, &v.EpisodeID, &v.EpisodeTitle); err != nil {
			return nil, err
		}
		v.Vec = blobToFloats(b)
		out = append(out, v)
	}
	return out, rows.Err()
}

// AddCorrectionID is AddCorrection returning the new id.
func (s *Store) AddCorrectionID(versionID int64, c Correction) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO corrections(version_id,kind,start_ms,end_ms,from_label,label,created_at,auto,score,text,origin,user_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, versionID, c.Kind, c.StartMs, c.EndMs, c.From, c.Label, time.Now().Unix(), c.Auto, c.Score, c.Text, c.Origin, c.UserID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) Correction(versionID, id int64) (Correction, error) {
	cs, err := s.Corrections(versionID)
	if err != nil {
		return Correction{}, err
	}
	for _, c := range cs {
		if c.ID == id {
			return c, nil
		}
	}
	return Correction{}, errors.New("correction not found")
}

// ReplaceAutoCorrections swaps a version's automatic corrections of one kind for
// new ones in one transaction: if anything fails the old ones are still there
// (a re-run that can't finish must never leave a transcript without its names).
func (s *Store) ReplaceAutoCorrections(versionID int64, kind string, cs []Correction) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM corrections WHERE version_id=? AND auto=1 AND kind=?`, versionID, kind); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, c := range cs {
		if _, err := tx.Exec(`INSERT INTO corrections(version_id,kind,start_ms,end_ms,from_label,label,created_at,auto,score,text,origin,user_id)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, versionID, c.Kind, c.StartMs, c.EndMs, c.From, c.Label, now, c.Auto, c.Score, c.Text, c.Origin, c.UserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteAutoCorrectionsKind removes automatic corrections of one kind
// ("merge" = voice merging, "range" = speaker identification).
func (s *Store) DeleteAutoCorrectionsKind(versionID int64, kind string) error {
	_, err := s.db.Exec(`DELETE FROM corrections WHERE version_id=? AND auto=1 AND kind=?`, versionID, kind)
	return err
}

// personOptions returns people ordered for a dropdown: the feed's hosts,
// then its regulars, then everyone else.
type personOption struct {
	ID    int64
	Label int
	Name  string
	Role  string // host / pool / ""
}

func (s *Store) PersonOptions(feedID int64) []personOption {
	ps, _ := s.People()
	roster, _ := s.Roster(feedID)
	var out []personOption
	for _, p := range ps {
		out = append(out, personOption{ID: p.ID, Label: personLabel(p.ID), Name: p.Name, Role: roster[p.ID]})
	}
	rank := func(r string) int {
		switch r {
		case roleHost:
			return 0
		case rolePool:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Role) < rank(out[j].Role) })
	return out
}
