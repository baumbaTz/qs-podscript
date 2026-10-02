package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server side of "transcribe for the server" (server mode only).
//
// A connected QS-PodScript logs in once (POST /api/v1/login) and gets an API
// token for that computer. With it the worker claims the next episode (a
// lease: the episode is reserved for it for a while), transcribes it with its
// own graphics card and uploads the raw result plus its compact audio copy.
// The server checks it, stores it as a new version, carries corrections over
// and recognizes people - exactly like a local transcription.
// The same token is used by the local app's pass-through (server podcasts
// shown at 127.0.0.1).

const (
	leaseFirst      = 3 * time.Hour // time to finish before the episode is handed out again
	leaseExtend     = 2 * time.Hour // each progress report pushes the end out this far
	leaseMaxFails   = 3             // after this many failed attempts: status "error"
	minWorkerVer    = "0.13.0"      // older apps are refused (their results would differ)
	apiVersionHdr   = "X-QSPodScript-Version"
	localIdleHeader = "X-QSPodScript-Local-Idle" // pass-through: this computer takes the clicked work now
	localAppHeader  = "X-QSPodScript-Local"      // set by the pass-through: link back to the local app
)

var remoteSchema = []string{
	`CREATE TABLE api_tokens(
		id         INTEGER PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name       TEXT NOT NULL DEFAULT '',
		token_hash TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL DEFAULT 0)`,
	`ALTER TABLE episodes ADD COLUMN lease_hash TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE episodes ADD COLUMN lease_user INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE episodes ADD COLUMN lease_expires INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE episodes ADD COLUMN lease_fails INTEGER NOT NULL DEFAULT 0`,
}

// ---------------------------------------------------------------- wire format

// Job is what a worker gets to transcribe.
type Job struct {
	Lease       string      `json:"lease"`
	EpisodeID   int64       `json:"episode_id"`
	Title       string      `json:"title"`
	Podcast     string      `json:"podcast"`
	AudioURL    string      `json:"audio_url"`
	Language    string      `json:"language"`
	NumSpeakers int         `json:"num_speakers"`
	VAD         bool        `json:"vad"`
	Diarize     DiarizeOpts `json:"diarize"`
	Pieces      bool        `json:"pieces"`
	// Kind "" = transcribe from AudioURL (feed audio). "diarize" = only
	// speaker detection on the server's audio copy (AudioURL relative to the
	// server, e.g. /audio/ep5_v9.ogg); the transcript stays (from SourceVersion).
	Kind          string `json:"kind,omitempty"`
	SourceVersion int64  `json:"source_version,omitempty"`
}

const (
	jobDiarize       = "diarize"
	minDiarizeWorker = "0.19.0" // workers that understand diarize jobs
)

// JobResult is what a worker uploads (plus the audio copy as a second part).
type JobResult struct {
	Kind           string             `json:"kind,omitempty"`
	AppVersion     string             `json:"app_version"`
	WhisperModel   string             `json:"whisper_model"`
	WhisperBackend string             `json:"whisper_backend"`
	Language       string             `json:"language"`
	AudioSeconds   float64            `json:"audio_seconds"`
	NumClusters    int                `json:"num_clusters"`
	DiarizeInfo    string             `json:"diarize_info"`
	Timing         string             `json:"timing"`
	Segments       []Segment          `json:"segments"`
	Tokens         []Token            `json:"tokens"`
	Turns          []Turn             `json:"turns"`
	Embeddings     []ClusterEmbedding `json:"embeddings"`
}

// ---------------------------------------------------------------- tokens

func (s *Store) NewAPIToken(userID int64, name string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := "qsps_" + base64.RawURLEncoding.EncodeToString(b)
	_, err := s.db.Exec(`INSERT INTO api_tokens(user_id,name,token_hash,created_at) VALUES(?,?,?,?)`,
		userID, strings.TrimSpace(name), tokenHash(tok), time.Now().Unix())
	return tok, err
}

func (s *Store) TokenUser(tok string) *User {
	if !strings.HasPrefix(tok, "qsps_") {
		return nil
	}
	h := tokenHash(tok)
	u, _, err := s.userByQuery(`id=(SELECT user_id FROM api_tokens WHERE token_hash=?)`, h)
	if err != nil {
		return nil
	}
	if tokenSeenDue(h) { // not on every request: a write per page part slowed everything down
		s.db.Exec(`UPDATE api_tokens SET last_seen=? WHERE token_hash=?`, time.Now().Unix(), h)
	}
	return u
}

type APIToken struct {
	ID        int64
	Name      string
	CreatedAt int64
	LastSeen  int64
}

func (s *Store) APITokens(userID int64) []APIToken {
	rows, err := s.db.Query(`SELECT id,name,created_at,last_seen FROM api_tokens WHERE user_id=? ORDER BY id`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastSeen)
		out = append(out, t)
	}
	return out
}

func (s *Store) tokenName(tok string) string {
	var n string
	s.db.QueryRow(`SELECT name FROM api_tokens WHERE token_hash=?`, tokenHash(tok)).Scan(&n)
	return n
}

func (s *Store) DeleteAPIToken(userID, id int64) error {
	_, err := s.db.Exec(`DELETE FROM api_tokens WHERE id=? AND user_id=?`, id, userID)
	return err
}

func bearerToken(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimSpace(a[len("Bearer "):])
	}
	return ""
}

// ---------------------------------------------------------------- leases

// ClaimLease reserves the next waiting episode (or exactly episodeID, if > 0).
// claimFilter: what a helper asks for - next in line (zero), one episode, or
// the next episode of one podcast.
type claimFilter struct {
	EpisodeID int64   `json:"episode_id,omitempty"`
	FeedID    int64   `json:"feed_id,omitempty"`
	Skip      []int64 `json:"skip,omitempty"` // episodes that just failed on this computer
}

func (s *Store) ClaimLease(userID int64, device, workerVersion string, want claimFilter) (*Job, error) {
	now := time.Now()
	// leases that ran out: back into the queue
	s.db.Exec(`UPDATE episodes SET status=CASE WHEN active_version_id IS NULL THEN 'new' ELSE 'queued' END,
		lease_hash='', lease_user=0, lease_expires=0, lease_fails=lease_fails+1
		WHERE status='leased' AND lease_expires<?`, now.Unix())
	s.db.Exec(`UPDATE episodes SET status=CASE WHEN active_version_id IS NULL THEN 'error' ELSE 'done' END,
		error='gave up after several failed attempts on workers', job_kind='', job_source=0
		WHERE status IN ('new','queued') AND lease_fails>=?`, leaseMaxFails)

	kinds := `job_kind=''`
	if versionAtLeast(workerVersion, minDiarizeWorker) {
		kinds = `job_kind IN ('','diarize')`
	}
	for try := 0; try < 5; try++ {
		only := ""
		if want.EpisodeID > 0 {
			only = fmt.Sprintf(" AND id=%d", want.EpisodeID)
		} else if want.FeedID > 0 {
			only = fmt.Sprintf(" AND feed_id=%d", want.FeedID)
		}
		if n := len(want.Skip); n > 0 && n <= 500 {
			ids := make([]string, n)
			for i, id := range want.Skip {
				ids[i] = strconv.FormatInt(id, 10)
			}
			only += " AND id NOT IN (" + strings.Join(ids, ",") + ")"
		}
		ep, err := scanEpisode(s.db.QueryRow(`SELECT ` + episodeCols + ` FROM episodes WHERE status IN ('new','queued') AND ` + kinds + only +
			` ORDER BY priority DESC, pub_date ASC, id ASC LIMIT 1`))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		kind, srcID := s.episodeJob(ep.ID)
		var src Version
		if kind == jobDiarize {
			src, err = s.Version(srcID)
			if err != nil || src.EpisodeID != ep.ID || src.Status != "done" || src.AudioFile == "" ||
				!fileExists(filepath.Join(P.Audio, src.AudioFile)) {
				// nothing to start from any more: transcribe it completely instead
				s.db.Exec(`UPDATE episodes SET job_kind='', job_source=0 WHERE id=?`, ep.ID)
				kind = ""
			}
		}
		b := make([]byte, 24)
		rand.Read(b)
		lease := base64.RawURLEncoding.EncodeToString(b)
		res, err := s.db.Exec(`UPDATE episodes SET status='leased', error='', lease_hash=?, lease_user=?, lease_expires=?,
			lease_device=?, lease_since=? WHERE id=? AND status IN ('new','queued')`,
			tokenHash(lease), userID, now.Add(leaseFirst).Unix(), device, now.Unix(), ep.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue // someone else was faster
		}
		f, err := s.Feed(ep.FeedID)
		if err != nil {
			return nil, err
		}
		job := &Job{
			Lease: lease, EpisodeID: ep.ID, Title: ep.Title, Podcast: f.Title, AudioURL: ep.AudioURL,
			Language: f.Language, NumSpeakers: f.NumSpeakers, VAD: f.VAD,
			Diarize: diarizeOptsFrom(s, f.NumSpeakers), Pieces: whisperChunked(s),
		}
		if kind == jobDiarize {
			job.Kind, job.SourceVersion, job.AudioURL = jobDiarize, src.ID, "/audio/"+src.AudioFile
		}
		return job, nil
	}
	return nil, errors.New("could not reserve an episode, try again")
}

// leaseEpisode finds the episode a lease belongs to (only while it is valid).
func (s *Store) leaseEpisode(lease string, userID int64) (Episode, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM episodes WHERE status='leased' AND lease_hash=? AND lease_user=? AND lease_expires>=?`,
		tokenHash(lease), userID, time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Episode{}, errors.New("this reservation has run out or belongs to someone else")
	}
	if err != nil {
		return Episode{}, err
	}
	return s.Episode(id)
}

func (s *Store) ExtendLease(lease string, userID int64) error {
	ep, err := s.leaseEpisode(lease, userID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE episodes SET lease_expires=? WHERE id=?`, time.Now().Add(leaseExtend).Unix(), ep.ID)
	return err
}

func (s *Store) FailLease(lease string, userID int64, msg string) error {
	ep, err := s.leaseEpisode(lease, userID)
	if err != nil {
		return err
	}
	// another computer may try again - until too many failed
	_, err = s.db.Exec(`UPDATE episodes SET
		status=CASE WHEN lease_fails+1>=? THEN (CASE WHEN active_version_id IS NULL THEN 'error' ELSE 'done' END)
			ELSE (CASE WHEN active_version_id IS NULL THEN 'new' ELSE 'queued' END) END,
		job_kind=CASE WHEN lease_fails+1>=? THEN '' ELSE job_kind END,
		error=?, lease_hash='', lease_user=0, lease_expires=0, lease_fails=lease_fails+1 WHERE id=?`,
		leaseMaxFails, leaseMaxFails, "worker: "+msg, ep.ID)
	return err
}

// episodeJob: what kind of work is waiting for an episode ("" = transcribe).
func (s *Store) episodeJob(id int64) (kind string, source int64) {
	s.db.QueryRow(`SELECT job_kind, job_source FROM episodes WHERE id=?`, id).Scan(&kind, &source)
	return
}

// QueueRediarize: a helper's computer redoes the speaker detection of a
// version (server without its own transcription).
func (s *Store) QueueRediarize(episodeID, sourceVersion int64) error {
	res, err := s.db.Exec(`UPDATE episodes SET status='queued', priority=`+topPriority+`, error='', job_kind='diarize', job_source=?, lease_fails=0
		WHERE id=? AND status NOT IN ('processing','leased')`, sourceVersion, episodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("this episode is being worked on right now")
	}
	return nil
}

// ---------------------------------------------------------------- storing a result

// storeJobResult checks an uploaded result and stores it as a new version.
func storeJobResult(ctx context.Context, st *Store, ep Episode, res *JobResult, audioPath string, userID int64, worker, device string) (Version, error) {
	if len(res.Segments) == 0 || res.AudioSeconds < 1 {
		return Version{}, errors.New("empty result")
	}
	if p := transcriptProblem(res.Segments, res.AudioSeconds); p != "" {
		return Version{}, fmt.Errorf("transcript unusable: %s", p)
	}
	f, err := st.Feed(ep.FeedID)
	if err != nil {
		return Version{}, err
	}
	v := Version{EpisodeID: ep.ID, WhisperModel: res.WhisperModel, WhisperBackend: res.WhisperBackend, Language: f.Language,
		TranscribedBy: userID, TranscribedOn: device}
	if v.ID, err = st.CreateVersion(v); err != nil {
		return v, err
	}
	fail := func(e error) (Version, error) { st.DeleteVersion(v.ID); return v, e }
	if err := st.SaveTranscription(v.ID, res.Segments, res.Tokens); err != nil {
		return fail(err)
	}
	if err := st.SaveDiarization(v.ID, res.Turns, res.Embeddings); err != nil {
		return fail(err)
	}
	v.AudioSeconds, v.NumClusters, v.DiarizeInfo = res.AudioSeconds, res.NumClusters, res.DiarizeInfo
	v.Timing = strings.TrimSpace(res.Timing + ", worker " + worker)
	if audioPath != "" {
		name := fmt.Sprintf("ep%d_v%d.ogg", ep.ID, v.ID)
		if err := os.Rename(audioPath, filepath.Join(P.Audio, name)); err != nil {
			return fail(err)
		}
		v.AudioFile = name
	}
	// same steps as after a local transcription
	var prev Version
	if ep.ActiveVersionID.Valid {
		if pv, e := st.Version(ep.ActiveVersionID.Int64); e == nil && pv.Status == "done" && hasManualCorrections(st, pv.ID) {
			prev = pv
		}
	}
	runVoiceMerge(st, &v)
	if prev.ID != 0 {
		if d := v.AudioSeconds - prev.AudioSeconds; d > 0.5 || d < -0.5 {
			logf("   note: the worker's audio differs from the checked version by %.1fs (dynamic ads?) - carried corrections may be off", d)
		}
		if n, e := carryCorrections(st, prev, v); e == nil && n > 0 {
			logf("   carried over %d corrections from version %d", n, prev.ID)
		}
	}
	if v.AudioFile != "" && fileExists(embeddingModelPath()) {
		runIdentify(ctx, st, &v, nil)
	}
	v.Status = "done"
	if err := st.FinishVersion(v); err != nil {
		return v, err
	}
	if err := st.FinishEpisode(ep.ID, v.ID); err != nil {
		return v, err
	}
	st.db.Exec(`UPDATE episodes SET lease_hash='', lease_user=0, lease_expires=0, lease_fails=0 WHERE id=?`, ep.ID)
	updateStatus(func(s *Status) { s.DoneCount++ })
	return v, nil
}

// storeDiarizeResult: new speaker detection from a helper for the transcript
// of an existing version (like "Redo speaker detection" locally).
func storeDiarizeResult(ctx context.Context, st *Store, ep Episode, srcID int64, res *JobResult, userID int64, worker, device string) (Version, error) {
	src, err := st.Version(srcID)
	if err != nil || src.EpisodeID != ep.ID || src.Status != "done" {
		return Version{}, errors.New("the version to start from is gone")
	}
	if len(res.Turns) == 0 {
		return Version{}, errors.New("no speakers detected")
	}
	if d := res.AudioSeconds - src.AudioSeconds; src.AudioSeconds > 0 && (d > 2 || d < -2) {
		return Version{}, fmt.Errorf("the audio length doesn't match (%.0fs instead of %.0fs)", res.AudioSeconds, src.AudioSeconds)
	}
	v := Version{EpisodeID: ep.ID, WhisperModel: src.WhisperModel, WhisperBackend: src.WhisperBackend, Language: src.Language,
		AudioFile: src.AudioFile, TranscribedBy: src.TranscribedBy, TranscribedOn: src.TranscribedOn,
		SpeakersBy: userID, SpeakersOn: device}
	if v.ID, err = st.CreateVersion(v); err != nil {
		return v, err
	}
	fail := func(e error) (Version, error) { st.DeleteVersion(v.ID); return v, e }
	if err := st.CopyTranscription(src.ID, v.ID); err != nil {
		return fail(err)
	}
	if err := st.SaveDiarization(v.ID, res.Turns, res.Embeddings); err != nil {
		return fail(err)
	}
	v.AudioSeconds, v.NumClusters, v.DiarizeInfo = src.AudioSeconds, res.NumClusters, res.DiarizeInfo
	v.Timing = strings.TrimSpace(fmt.Sprintf("transcript from v%d, %s, speakers by %s", src.ID, res.Timing, worker))
	runVoiceMerge(st, &v)
	if n, e := carryCorrections(st, src, v); e == nil && n > 0 {
		logf("   carried over %d corrections from version %d", n, src.ID)
	}
	if fileExists(embeddingModelPath()) {
		runIdentify(ctx, st, &v, nil)
	}
	v.Status = "done"
	if err := st.FinishVersion(v); err != nil {
		return v, err
	}
	if err := st.FinishEpisode(ep.ID, v.ID); err != nil {
		return v, err
	}
	st.db.Exec(`UPDATE episodes SET lease_hash='', lease_user=0, lease_expires=0, lease_fails=0 WHERE id=?`, ep.ID)
	updateStatus(func(s *Status) { s.DoneCount++ })
	return v, nil
}

// ---------------------------------------------------------------- HTTP handlers

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// apiUser: the user behind the bearer token (API calls don't use cookies).
func (s *Server) apiUser(w http.ResponseWriter, r *http.Request) *User {
	u := currentUser(r)
	if u == nil || bearerToken(r) == "" {
		apiError(w, http.StatusUnauthorized, "not connected - log in again from your QS-PodScript")
		return nil
	}
	return u
}

func versionAtLeast(have, want string) bool {
	pa, pb := strings.Split(have, "."), strings.Split(want, ".")
	for i := 0; i < 3; i++ {
		var a, b int
		if i < len(pa) {
			a, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			b, _ = strconv.Atoi(pb[i])
		}
		if a != b {
			return a > b
		}
	}
	return true
}

func (s *Server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Password, Device string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		apiError(w, http.StatusBadRequest, "bad request")
		return
	}
	ip := clientIP(r)
	if tooManyFails(ip) {
		apiError(w, http.StatusTooManyRequests, "too many failed attempts, try again in 15 minutes")
		return
	}
	u, hash, err := s.st.userByQuery(`name=?`, strings.TrimSpace(in.Name))
	if err != nil || !checkPassword(hash, in.Password) {
		noteFail(ip)
		time.Sleep(700 * time.Millisecond)
		apiError(w, http.StatusUnauthorized, "name or password is wrong")
		return
	}
	dev := strings.TrimSpace(in.Device)
	if dev == "" {
		dev = "QS-PodScript"
	}
	tok, err := s.st.NewAPIToken(u.ID, dev)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.st.Audit(u.ID, actConnect, 0, 0, 0, dev)
	logf("Connected: %s from %s (%s)", u.Name, dev, ip)
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "user": u.Name, "admin": u.IsAdmin(), "server_version": version})
}

type apiPodcast struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Episodes    int    `json:"episodes"`
	Transcribed int    `json:"transcribed"`
	CanEdit     bool   `json:"can_edit"`
}

func (s *Server) handleAPIPodcasts(w http.ResponseWriter, r *http.Request) {
	feeds, err := s.st.Feeds()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var out []apiPodcast
	for _, f := range feeds {
		c, _ := s.st.StatusCounts(f.ID)
		total := 0
		for _, n := range c {
			total += n
		}
		out = append(out, apiPodcast{ID: f.ID, Title: f.Title, Episodes: total, Transcribed: c["done"], CanEdit: s.canEdit(r, f.ID)})
	}
	u := currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{"podcasts": out, "server_version": version, "admin": u != nil && u.IsAdmin()})
}

func (s *Server) handleAPIClaim(w http.ResponseWriter, r *http.Request) {
	u := s.apiUser(w, r)
	if u == nil {
		return
	}
	if v := r.Header.Get(apiVersionHdr); !versionAtLeast(v, minWorkerVer) {
		apiError(w, http.StatusUpgradeRequired, fmt.Sprintf("please update QS-PodScript to %s or newer to transcribe for this server", minWorkerVer))
		return
	}
	var in claimFilter
	json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) // empty body = next in line
	job, err := s.st.ClaimLease(u.ID, s.st.tokenName(bearerToken(r)), r.Header.Get(apiVersionHdr), in)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if job == nil {
		writeJSON(w, http.StatusOK, map[string]any{"job": nil})
		return
	}
	logf("Episode %d (%s) handed to %s", job.EpisodeID, job.Title, whoOn(u.Name, s.st.tokenName(bearerToken(r))))
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (s *Server) handleAPIProgress(w http.ResponseWriter, r *http.Request) {
	u := s.apiUser(w, r)
	if u == nil {
		return
	}
	if err := s.st.ExtendLease(r.PathValue("lease"), u.ID); err != nil {
		apiError(w, http.StatusGone, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAPIFail(w http.ResponseWriter, r *http.Request) {
	u := s.apiUser(w, r)
	if u == nil {
		return
	}
	var in struct{ Error string }
	json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in)
	var epID int64
	var epTitle string
	s.st.db.QueryRow(`SELECT id, title FROM episodes WHERE lease_hash=?`, tokenHash(r.PathValue("lease"))).Scan(&epID, &epTitle)
	if err := s.st.FailLease(r.PathValue("lease"), u.ID, in.Error); err != nil {
		apiError(w, http.StatusGone, err.Error())
		return
	}
	s.st.Audit(u.ID, actJobFailed, 0, 0, 0, in.Error)
	who := whoOn(u.Name, s.st.tokenName(bearerToken(r)))
	if epID > 0 {
		logf("Episode %d (%s) failed – %s: %s", epID, epTitle, who, in.Error)
	} else {
		logf("%s could not transcribe: %s", who, in.Error)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAPIResult: multipart with "result" (JSON) and "audio" (ogg).
func (s *Server) handleAPIResult(w http.ResponseWriter, r *http.Request) {
	u := s.apiUser(w, r)
	if u == nil {
		return
	}
	lease := r.PathValue("lease")
	ep, err := s.st.leaseEpisode(lease, u.ID)
	if err != nil {
		apiError(w, http.StatusGone, err.Error())
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		apiError(w, http.StatusBadRequest, "expected multipart upload")
		return
	}
	var res *JobResult
	audio := filepath.Join(P.Work, fmt.Sprintf("upload-ep%d-%d.ogg", ep.ID, time.Now().UnixNano()))
	defer os.Remove(audio)
	gotAudio := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			apiError(w, http.StatusBadRequest, "upload broken: "+err.Error())
			return
		}
		switch part.FormName() {
		case "result":
			res = &JobResult{}
			if err := json.NewDecoder(io.LimitReader(part, 512<<20)).Decode(res); err != nil {
				apiError(w, http.StatusBadRequest, "result unreadable: "+err.Error())
				return
			}
		case "audio":
			f, err := os.Create(audio)
			if err != nil {
				apiError(w, http.StatusInternalServerError, err.Error())
				return
			}
			n, err := io.Copy(f, io.LimitReader(part, 2<<30))
			f.Close()
			if err != nil {
				apiError(w, http.StatusBadRequest, "audio upload broken: "+err.Error())
				return
			}
			gotAudio = n > 0
		}
		part.Close()
	}
	kind, srcID := s.st.episodeJob(ep.ID)
	if res == nil || (!gotAudio && kind != jobDiarize) {
		apiError(w, http.StatusBadRequest, "result or audio missing")
		return
	}
	device := s.st.tokenName(bearerToken(r))
	var v Version
	if kind == jobDiarize {
		if res.Kind != jobDiarize {
			err = errors.New("expected a speaker detection result")
		} else {
			v, err = storeDiarizeResult(r.Context(), s.st, ep, srcID, res, u.ID, u.Name, device)
		}
	} else {
		v, err = storeJobResult(r.Context(), s.st, ep, res, audio, u.ID, u.Name, device)
	}
	if err != nil {
		s.st.FailLease(lease, u.ID, err.Error())
		s.st.Audit(u.ID, actJobRejected, ep.FeedID, ep.ID, 0, err.Error())
		logf("Result from %s for episode %d rejected: %v", whoOn(u.Name, device), ep.ID, err)
		apiError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if kind == jobDiarize {
		s.st.Audit(u.ID, actDiarized, ep.FeedID, ep.ID, v.ID, device)
		logf("Episode %d (%s): speaker detection redone by %s: version %d", ep.ID, ep.Title, whoOn(u.Name, device), v.ID)
	} else {
		s.st.Audit(u.ID, actTranscribed, ep.FeedID, ep.ID, v.ID, device)
		logf("Episode %d (%s) transcribed by %s: version %d", ep.ID, ep.Title, whoOn(u.Name, device), v.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": v.ID})
}

// ---------------------------------------------------------------- reservations overview (admins)

type reservation struct {
	EpisodeID int64
	Title     string
	Podcast   string
	User      string
	Device    string
	Since     int64
	Expires   int64
	Fails     int
}

func (s *Store) Reservations() []reservation {
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, COALESCE(u.name,'?'), e.lease_device, e.lease_since, e.lease_expires, e.lease_fails
		FROM episodes e JOIN feeds f ON f.id=e.feed_id LEFT JOIN users u ON u.id=e.lease_user
		WHERE e.status='leased' ORDER BY e.lease_since`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []reservation
	for rows.Next() {
		var r reservation
		rows.Scan(&r.EpisodeID, &r.Title, &r.Podcast, &r.User, &r.Device, &r.Since, &r.Expires, &r.Fails)
		out = append(out, r)
	}
	return out
}

// ReleaseLease puts a reserved episode back into the queue (the worker's
// upload will then be refused). Not counted as a failure.
func (s *Store) ReleaseLease(episodeID int64) error {
	_, err := s.db.Exec(`UPDATE episodes SET status=CASE WHEN active_version_id IS NULL THEN 'new' ELSE 'queued' END,
		lease_hash='', lease_user=0, lease_expires=0, lease_device='', lease_since=0 WHERE id=? AND status='leased'`, episodeID)
	return err
}

// failedEpisodes: episodes that failed on workers (for the overview).
func (s *Store) failedOnWorkers(limit int) []reservation {
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, e.error, e.lease_fails FROM episodes e JOIN feeds f ON f.id=e.feed_id
		WHERE e.status='error' AND e.lease_fails>0 ORDER BY e.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []reservation
	for rows.Next() {
		var r reservation
		rows.Scan(&r.EpisodeID, &r.Title, &r.Podcast, &r.Device, &r.Fails) // Device = error text here
		out = append(out, r)
	}
	return out
}

type waitingJob struct {
	EpisodeID int64
	Title     string
	Podcast   string
	Kind      string // what the helper will do
	First     bool   // asked for explicitly (goes first)
}

// waitingJobs: the queue for the helpers, in the order they get it.
func (s *Store) waitingJobs(limit int) []waitingJob {
	rows, err := s.db.Query(`SELECT e.id, e.title, f.title, e.job_kind, e.priority FROM episodes e JOIN feeds f ON f.id=e.feed_id
		WHERE e.status IN ('new','queued') ORDER BY e.priority DESC, e.pub_date ASC, e.id ASC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []waitingJob
	for rows.Next() {
		var j waitingJob
		var kind string
		var prio int
		rows.Scan(&j.EpisodeID, &j.Title, &j.Podcast, &kind, &prio)
		j.Kind, j.First = "transcription", prio >= 100
		if kind == jobDiarize {
			j.Kind = "speaker detection only"
		}
		out = append(out, j)
	}
	return out
}

func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	var waiting int
	s.st.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE status IN ('new','queued')`).Scan(&waiting)
	s.render(w, r, "work", "Helpers' work", "users", map[string]any{
		"Reserved": s.st.Reservations(), "Failed": s.st.failedOnWorkers(50), "Waiting": waiting, "Now": time.Now().Unix(),
		"Queue": s.st.waitingJobs(30), "SelfTranscribes": !s.helpersOnly(),
	})
}

func (s *Server) handleWorkRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	retry := r.FormValue("retry") == "1"
	var err error
	if retry { // failed episode: give it another round of attempts
		_, err = s.st.db.Exec(`UPDATE episodes SET status='new', error='', lease_fails=0 WHERE id=? AND status='error'`, id)
	} else {
		err = s.st.ReleaseLease(id)
	}
	if err != nil {
		back(w, r, "/work", "", err.Error())
		return
	}
	s.st.Audit(currentUser(r).ID, actUserAdmin, 0, id, 0, "handed an episode back to the queue")
	back(w, r, "/work", "The episode is back in the queue.", "")
}

var tokenSeen = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// tokenSeenDue: record "last seen" at most once a minute per key.
func tokenSeenDue(h string) bool {
	tokenSeen.Lock()
	defer tokenSeen.Unlock()
	if t, ok := tokenSeen.m[h]; ok && time.Since(t) < time.Minute {
		return false
	}
	tokenSeen.m[h] = time.Now()
	return true
}

// whoOn: "batz on computer office-pc" for the activity log (which of a
// user's computers did it).
func whoOn(user, device string) string {
	if device == "" {
		return user
	}
	return user + " on computer “" + device + "”"
}
