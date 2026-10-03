package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Local side of the homeserver connections (normal local mode; the saved
// servers and the active place are in servers.go).
//
//   - Connect: server address + name + password -> an API token for this
//     computer, stored in the settings (the password is not stored).
//   - Transcribe for the server: when the own queue is empty, the worker takes
//     episodes from the server, transcribes them here and uploads the result.
//   - Pass-through: the server's pages are shown at 127.0.0.1 (a second local
//     port) with this computer's login, so editing server podcasts works
//     exactly like local ones. Nothing is copied: every change goes straight
//     to the server.

// remoteConf: one saved server (servers.go).
type remoteConf struct {
	ID    int64  `json:"id"`
	Name  string `json:"name,omitempty"` // shown in the place menu ("" = the address)
	URL   string `json:"url"`
	Token string `json:"token"`
	User  string `json:"user"`
	Admin string `json:"admin,omitempty"` // "1"/"0": admin on the server ("" = not known yet)
	Work  bool   `json:"work,omitempty"`  // "Transcribe for this server"
}

func normalizeServerURL(u string) (string, error) {
	u = strings.TrimSpace(u)
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https") {
		return "", errors.New("that is not a server address (e.g. https://podscript.example.org)")
	}
	pu.Path, pu.RawQuery, pu.Fragment = strings.TrimRight(pu.Path, "/"), "", ""
	return pu.String(), nil
}

var apiClient = &http.Client{Timeout: 30 * time.Second}

// apiCall sends JSON to the server and decodes the JSON answer.
func apiCall(ctx context.Context, c remoteConf, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(apiVersionHdr, version)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return fmt.Errorf("server not reachable: %v", err)
	}
	defer resp.Body.Close()
	return decodeAPI(resp, out)
}

// apiStatusError: the server answered, but with an error status.
type apiStatusError struct {
	Code int
	Msg  string
}

func (e *apiStatusError) Error() string { return e.Msg }

// permanent: retrying won't help (the server refused the request itself).
func (e *apiStatusError) permanent() bool {
	return e.Code >= 400 && e.Code < 500 && e.Code != http.StatusRequestTimeout && e.Code != http.StatusTooManyRequests
}

// the version each server last announced ("" = unknown or older server)
var serverVersions sync.Map // "https://host" -> version

func noteServerVersion(resp *http.Response) {
	if v := resp.Header.Get(apiVersionHdr); v != "" && len(v) < 40 && resp.Request != nil {
		serverVersions.Store(resp.Request.URL.Scheme+"://"+resp.Request.URL.Host, v)
	}
}

func seenServerVersion(c remoteConf) string {
	if u, err := url.Parse(c.URL); err == nil {
		if v, ok := serverVersions.Load(u.Scheme + "://" + u.Host); ok {
			return v.(string)
		}
	}
	return ""
}

func decodeAPI(resp *http.Response, out any) error {
	noteServerVersion(resp)
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return &apiStatusError{resp.StatusCode, "server: " + e.Error}
		}
		return &apiStatusError{resp.StatusCode, "server answered " + resp.Status}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("unexpected answer from the server (is this a QS-PodScript server?): %v", err)
		}
	}
	return nil
}

// connectRemote logs in and saves the server (or renews its key); returns its id.
func connectRemote(ctx context.Context, st *Store, serverURL, name, password string) (int64, error) {
	u, err := normalizeServerURL(serverURL)
	if err != nil {
		return 0, err
	}
	host, _ := os.Hostname()
	var out struct {
		Token, User string
		Admin       bool
	}
	if err := apiCall(ctx, remoteConf{URL: u}, http.MethodPost, "/api/v1/login",
		map[string]string{"name": name, "password": password, "device": host}, &out); err != nil {
		return 0, err
	}
	if out.Token == "" {
		return 0, errors.New("the server gave no login token")
	}
	return addServer(st, remoteConf{URL: u, Token: out.Token, User: out.User,
		Admin: map[bool]string{true: "1", false: "0"}[out.Admin]}), nil
}

// refreshServerAdmin asks the server whether we are (still) an admin there
// - at most every 30 seconds per server.
var adminChecked sync.Map // server id -> time.Time

func refreshServerAdmin(ctx context.Context, st *Store, c remoteConf) {
	if t, ok := adminChecked.Load(c.ID); ok && time.Since(t.(time.Time)) < 30*time.Second {
		return
	}
	adminChecked.Store(c.ID, time.Now())
	var out struct {
		Admin *bool `json:"admin"`
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if apiCall(ctx, c, http.MethodGet, "/api/v1/podcasts", nil, &out) == nil && out.Admin != nil {
		v := map[bool]string{true: "1", false: "0"}[*out.Admin]
		if v != c.Admin {
			updateServer(st, c.ID, func(x *remoteConf) { x.Admin = v })
		}
	}
}

// ---------------------------------------------------------------- transcribing for the server

// processRemoteJob takes one episode from the server, transcribes it here and
// uploads the result. did = false when the server had nothing to do.
func processRemoteJob(ctx context.Context, st *Store) (did bool, err error) {
	c, job, err := claimRemoteJob(ctx, st)
	if err != nil || job == nil {
		return false, err
	}
	return true, runRemoteJob(ctx, st, c, job)
}

// claimTurn: which ticked server is asked first next time (they take turns).
var claimTurn atomic.Int64

// claimRemoteJob asks the ticked servers for work, taking turns (nil = none
// of them has anything / not helping). An error only when no server could
// be asked at all.
func claimRemoteJob(ctx context.Context, st *Store) (remoteConf, *Job, error) {
	var ticked []remoteConf
	for _, c := range savedServers(st) {
		if c.Connected() && c.Work {
			ticked = append(ticked, c)
		}
	}
	if len(ticked) == 0 {
		return remoteConf{}, nil, nil
	}
	start := int(claimTurn.Load() % int64(len(ticked)))
	var firstErr error
	failed := 0
	for k := 0; k < len(ticked); k++ {
		c := ticked[(start+k)%len(ticked)]
		var claim struct {
			Job *Job `json:"job"`
		}
		if err := apiCall(ctx, c, http.MethodPost, "/api/v1/work/claim", map[string]string{}, &claim); err != nil {
			if ctx.Err() != nil {
				return c, nil, err
			}
			logf("   server %s: %v", c.Label(), err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", c.Label(), err)
			}
			failed++
			continue
		}
		if claim.Job != nil {
			claimTurn.Store(int64(start + k + 1)) // the next one goes first next time
			return c, claim.Job, nil
		}
	}
	if failed == len(ticked) {
		return remoteConf{}, nil, firstErr
	}
	return remoteConf{}, nil, nil
}

// releaseRemoteJob gives a claimed job back (this computer can't do it now).
func releaseRemoteJob(c remoteConf, job *Job, why string) {
	fctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	apiCall(fctx, c, http.MethodPost, "/api/v1/work/"+job.Lease+"/fail", map[string]string{"error": why}, nil)
}

func runRemoteJob(ctx context.Context, st *Store, c remoteConf, job *Job) (err error) {
	label := fmt.Sprintf("server-ep%d", job.EpisodeID)
	logf("")
	if job.Kind == jobDiarize {
		logf("== For the server %s: %s – %s (speaker detection only)", c.Label(), job.Podcast, job.Title)
	} else {
		logf("== For the server %s: %s – %s", c.Label(), job.Podcast, job.Title)
	}
	updateStatus(func(s *Status) {
		s.EpisodeID, s.EpisodeTitle, s.Stage, s.Pct = 0, "["+c.Label()+"] "+job.Title, "Starting", -1
		s.ServerEpisodeID, s.ServerID = job.EpisodeID, c.ID
	})
	defer updateStatus(func(s *Status) { s.EpisodeTitle, s.ServerEpisodeID, s.ServerID = "", 0, 0 })

	// keep the reservation alive while working
	pctx, stopPings := context.WithCancel(ctx)
	defer stopPings()
	go func() {
		wait := 10 * time.Minute
		for {
			select {
			case <-pctx.Done():
				return
			case <-time.After(wait):
			}
			if e := apiCall(pctx, c, http.MethodPost, "/api/v1/work/"+job.Lease+"/progress", map[string]string{}, nil); e != nil {
				if pctx.Err() != nil {
					return
				}
				// harmless while it's short: the reservation lasts hours
				logf("   note: could not reach the server to say \"still working\" (%v) - trying again in a minute", e)
				wait = time.Minute
			} else {
				wait = 10 * time.Minute
			}
		}
	}()

	work := filepath.Join(P.Work, label)
	os.MkdirAll(work, 0o755)
	defer os.RemoveAll(work)
	var res *JobResult
	var audio string
	switch job.Kind {
	case "":
		res, audio, err = transcribeJob(ctx, st, job, work, label)
	case jobDiarize:
		res, err = diarizeJob(ctx, st, c, job, work)
	default:
		err = fmt.Errorf("unknown kind of work %q - please update QS-PodScript", job.Kind)
	}
	if err != nil {
		if ctx.Err() == nil {
			// tell the server, so someone else can take it
			fctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			apiCall(fctx, c, http.MethodPost, "/api/v1/work/"+job.Lease+"/fail", map[string]string{"error": err.Error()}, nil)
			cancel()
		}
		// interrupted by the user: the reservation simply runs out on the server
		return err
	}
	setStage("Uploading to the server", -1)
	if err := uploadWithRetry(ctx, c, job.Lease, res, audio); err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	logf("   uploaded to the server")
	updateStatus(func(s *Status) { s.DoneCount++ })
	return nil
}

var episodeWorkPath = regexp.MustCompile(`^/episodes/(\d+)/(process|rediarize)$`)

// ---------------------------------------------------------------- doing one episode on request
// Clicking "Transcribe again" / "Redo speaker detection" on a server page
// opened through this app means: this computer does that episode - now if
// it is idle, otherwise right after the current work (asks.go). Nothing else
// starts by itself: running through the server's waiting episodes happens
// only with "Start transcribing" and "Transcribe for the server" ticked.

type helperRequest struct {
	server    int64 // saved server id
	episodeID int64
	kind      string
}

var helperKickCh = make(chan helperRequest, 16)

// localIdle: the pass-through tells the server whether this computer can
// take the work right now (for the message shown after the click).
var localIdle = func() bool { return false }

func helperKick(server, episodeID int64, kind string) {
	select {
	case helperKickCh <- helperRequest{server, episodeID, kind}:
	default:
	}
}

func runHelperPoller(ctx context.Context, st *Store, w *Worker) {
	localIdle = func() bool { return !w.Busy() && getSetupState(st).Ready() }
	for {
		var req helperRequest
		select {
		case <-ctx.Done():
			return
		case req = <-helperKickCh:
		}
		c, ok := serverByID(st, req.server)
		if !ok {
			continue
		}
		a := ask{Server: true, SrvID: c.ID, ID: req.episodeID, Kind: req.kind}
		a.Title = serverPageTitle(ctx, c, a.Path())
		st.addAsk(a)
		if !w.Busy() && getSetupState(st).Ready() {
			w.StartAsks()
		}
	}
}

// jobDiarizeOpts: the server's speaker detection settings (own ones if this
// app doesn't know the server's model), model downloaded if needed.
func jobDiarizeOpts(ctx context.Context, st *Store, job *Job) (DiarizeOpts, error) {
	dopts := job.Diarize
	if diarizeModels[dopts.Model] == "" {
		dopts = diarizeOptsFrom(st, job.NumSpeakers)
	}
	if p := diarizeModelPath(dopts.Model); !fileExists(p) {
		logf("   downloading speaker detection model %s (the server uses it)", dopts.Model)
		if err := downloadFile(ctx, speakerModelsURL+filepath.Base(p), p, "speaker model "+dopts.Model); err != nil {
			return dopts, err
		}
	}
	return dopts, nil
}

// diarizeJob: only speaker detection, on the server's audio copy. The
// transcript stays on the server; only the voices go back (no audio upload).
func diarizeJob(ctx context.Context, st *Store, c remoteConf, job *Job, work string) (*JobResult, error) {
	if !fileExists(segmentationModelPath()) {
		return nil, errors.New("speaker detection models missing - finish the setup first")
	}
	dopts, err := jobDiarizeOpts(ctx, st, job)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(job.AudioURL, "/audio/") {
		return nil, fmt.Errorf("unexpected audio address %q", job.AudioURL)
	}
	t0 := time.Now()
	src := filepath.Join(work, "source.ogg")
	if err := downloadFile(ctx, strings.TrimRight(c.URL, "/")+job.AudioURL, src, "audio copy from the server"); err != nil {
		return nil, err
	}
	timing := []string{"download " + time.Since(t0).Round(time.Second).String()}
	setStage("Converting audio", -1)
	wav := filepath.Join(work, "audio.wav")
	if err := runFFmpeg(ctx, "-i", src, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		return nil, fmt.Errorf("convert audio: %w", err)
	}
	samples, err := readWav16k(wav)
	if err != nil {
		return nil, err
	}
	t0 = time.Now()
	setStage("Detecting speakers", -1)
	logf("   speaker detection: %s, on %s", dopts, providerText(providerNow()))
	turns, embs, err := diarizeWav(ctx, wav, dopts)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	clusters := map[int]bool{}
	for _, t := range turns {
		clusters[t.Cluster] = true
	}
	timing = append(timing, "diarize "+time.Since(t0).Round(time.Second).String())
	logf("   speakers: %d voices, %d turns in %s", len(clusters), len(turns), time.Since(t0).Round(time.Second))
	return &JobResult{
		Kind: jobDiarize, AppVersion: version, AudioSeconds: float64(len(samples)) / sampleRate,
		NumClusters: len(clusters), DiarizeInfo: dopts.String(), Timing: strings.Join(timing, ", "),
		Turns: turns, Embeddings: embs,
	}, nil
}

func transcribeJob(ctx context.Context, st *Store, job *Job, work, label string) (*JobResult, string, error) {
	modelName, modelPath := whisperModelPath(st)
	if !fileExists(modelPath) {
		return nil, "", errors.New("speech model missing - finish the setup first")
	}
	dopts, err := jobDiarizeOpts(ctx, st, job)
	if err != nil {
		return nil, "", err
	}
	feed := Feed{Title: job.Podcast, Language: job.Language, NumSpeakers: job.NumSpeakers, VAD: job.VAD}
	t0 := time.Now()
	src := filepath.Join(work, "source"+audioExt(job.AudioURL))
	if err := downloadFile(ctx, job.AudioURL, src, "episode audio"); err != nil {
		return nil, "", err
	}
	timing := []string{"download " + time.Since(t0).Round(time.Second).String()}
	setStage("Converting audio", -1)
	wav := filepath.Join(work, "audio.wav")
	if err := runFFmpeg(ctx, "-i", src, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav); err != nil {
		return nil, "", fmt.Errorf("convert audio: %w", err)
	}
	an, err := analyzeAudio(ctx, st, feed, dopts, modelName, modelPath, wav, work, label, false)
	if err != nil {
		return nil, "", err
	}
	timing = append(timing, an.Timing...)
	setStage("Saving audio copy", -1)
	ogg := filepath.Join(work, "audio.ogg")
	if err := runFFmpeg(ctx, "-i", wav, "-c:a", "libopus", "-b:a", "24k", "-ac", "1", ogg); err != nil {
		return nil, "", fmt.Errorf("audio copy: %w", err)
	}
	return &JobResult{
		AppVersion: version, WhisperModel: modelName, WhisperBackend: an.Device, Language: job.Language,
		AudioSeconds: an.AudioSeconds, NumClusters: an.NumClusters, DiarizeInfo: dopts.String(),
		Timing: strings.Join(timing, ", "), Segments: an.Segs, Tokens: an.Toks, Turns: an.Turns, Embeddings: an.Embs,
	}, ogg, nil
}

// uploadWithRetry: the work is done - don't lose it because the server is
// briefly unreachable (restart, update, network). Tries for about an hour;
// stops at once if the server refuses the result itself.
func uploadWithRetry(ctx context.Context, c remoteConf, lease string, res *JobResult, audioPath string) error {
	waits := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
		10 * time.Minute, 10 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i := 0; ; i++ {
		err := uploadResult(ctx, c, lease, res, audioPath)
		if err == nil {
			return nil
		}
		var se *apiStatusError
		if ctx.Err() != nil || (errors.As(err, &se) && se.permanent()) || i >= len(waits) {
			return err
		}
		logf("   could not upload to the server (%v) - trying again in %s", err, waits[i])
		setStage(fmt.Sprintf("Server not reachable – trying again in %s", waits[i]), -1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waits[i]):
		}
		setStage("Uploading to the server", -1)
	}
}

// uploadResult streams result.json + audio.ogg as one multipart request.
func uploadResult(ctx context.Context, c remoteConf, lease string, res *JobResult, audioPath string) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			part, err := mw.CreateFormFile("result", "result.json")
			if err != nil {
				return err
			}
			if err := json.NewEncoder(part).Encode(res); err != nil {
				return err
			}
			if audioPath == "" { // speaker detection only: nothing else to send
				return mw.Close()
			}
			part, err = mw.CreateFormFile("audio", "audio.ogg")
			if err != nil {
				return err
			}
			f, err := os.Open(audioPath)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(part, f); err != nil {
				return err
			}
			return mw.Close()
		}()
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/v1/work/"+lease+"/result", pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set(apiVersionHdr, version)
	resp, err := (&http.Client{}).Do(req) // no timeout: large upload on a slow line
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeAPI(resp, nil)
}

// ---------------------------------------------------------------- pass-through

type passThrough struct {
	mu   sync.Mutex
	addr string // "http://127.0.0.1:8323" once running
	srv  *http.Server
}

var proxy passThrough

// startPassThrough serves the pages of the active server on a second local
// port, logged in with this computer's key for that server. It runs as
// long as the app does; which server it shows is decided per request, so
// switching places needs no restart. With this computer active it sends
// the browser back to the local pages.
func startPassThrough(st *Store, localURL string, basePort int) (string, error) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	if proxy.srv != nil {
		return proxy.addr, nil
	}
	var ln net.Listener
	var err error
	for p := basePort; p < basePort+20; p++ {
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			break
		}
	}
	if ln == nil {
		return "", fmt.Errorf("no free local port for the server pages: %v", err)
	}
	self := "http://" + ln.Addr().String()
	localBase := strings.TrimRight(localURL, "/")
	// /_local/…: this computer's own job status, start/stop and the place
	// menu, shown on the server pages (same address, so no cross-site calls)
	local, err := url.Parse(localBase)
	if err != nil {
		ln.Close()
		return "", err
	}
	localRP := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(local)
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, "/_local")
			pr.Out.URL.RawPath = ""
			pr.Out.Host = local.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			if pr.In.Method == http.MethodPost { // already checked below: our own page
				pr.Out.Header.Set("Origin", "http://"+local.Host)
				pr.Out.Header.Del("Referer")
			}
		},
		FlushInterval: -1, // live status stream
	}
	serverRP := func(c remoteConf, target *url.URL) *httputil.ReverseProxy {
		return &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Host = target.Host
				pr.Out.Header.Set("Authorization", "Bearer "+c.Token)
				pr.Out.Header.Set(localAppHeader, localURL)
				pr.Out.Header.Set(apiVersionHdr, version)
				pr.Out.Header.Set(placesHeader, encodePlaces(placeList(st)))
				pr.Out.Header.Set(lookHeader, st.Setting("look", lookDefault))
				pr.Out.Header.Del("Cookie")
				// checked below; the server must not see our local origin
				pr.Out.Header.Del("Origin")
				pr.Out.Header.Del("Referer")
			},
			ModifyResponse: func(resp *http.Response) error {
				noteServerVersion(resp)
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Server not reachable</title>
<body style="font:16px system-ui;max-width:40rem;margin:3rem auto;padding:0 1rem">
<h1>The server is not reachable</h1><p>%s could not be reached (%s).</p>
<form method="post" action="/_local/switch"><input type="hidden" name="place" value="0">
<p>Your own podcasts keep working: <button type="submit">Switch to this computer</button> or <a href="%s/setup#places">open Setup</a>.</p></form>`,
					html.EscapeString(c.Label()), html.EscapeString(err.Error()), localBase)
			},
		}
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the key is added here, so another website must not be able to send
		// forms through this port: only our own pages may post
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o := r.Header.Get("Origin")
			ref := r.Header.Get("Referer")
			if (o != "" && o != self) || (o == "" && !strings.HasPrefix(ref, self+"/")) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/_local/") {
			switch r.Method + " " + r.URL.Path {
			case "GET /_local/events", "POST /_local/queue/start", "POST /_local/queue/stop", "POST /_local/switch":
				localRP.ServeHTTP(w, r)
			default:
				http.NotFound(w, r)
			}
			return
		}
		c, ok := activeServer(st)
		if !ok { // this computer is the active place
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, localBase+"/", http.StatusSeeOther)
				return
			}
			http.Error(w, "No server is active - switch to a server first.", http.StatusConflict)
			return
		}
		target, err := url.Parse(c.URL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		var askedFor int64 // "Transcribe again" / "Redo speaker detection" / "Transcribe this podcast" clicked here
		kind := ""
		if r.Method == http.MethodPost {
			if m := episodeWorkPath.FindStringSubmatch(r.URL.Path); m != nil {
				askedFor, _ = strconv.ParseInt(m[1], 10, 64)
				if m[2] == "rediarize" {
					kind = "diarize"
				}
			} else if r.URL.Path == "/queue/start" {
				// read the form, then hand the same body on to the server
				body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
				r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(body))
				r.ContentLength = int64(len(body))
				if v, err := url.ParseQuery(string(body)); err == nil {
					if id, _ := strconv.ParseInt(v.Get("feed"), 10, 64); id > 0 {
						askedFor, kind = id, askPodcast
					}
				}
			}
			if askedFor > 0 {
				r.Header.Set(localIdleHeader, map[bool]string{true: "1", false: "0"}[localIdle()])
			}
		}
		rec := &statusRecorder{ResponseWriter: w}
		serverRP(c, target).ServeHTTP(rec, r)
		if askedFor > 0 && rec.code < 400 {
			helperKick(c.ID, askedFor, kind) // this computer does it: now, or after the current work
		}
	})
	proxy.srv = &http.Server{Handler: h, ReadHeaderTimeout: 20 * time.Second}
	proxy.addr = self
	go proxy.srv.Serve(ln)
	debugf("server pages available at %s/", self)
	return self, nil
}

func passThroughAddr() string {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return proxy.addr
}

// statusRecorder (auth.go): let the proxy flush live streams through it
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

var pageTitleRe = regexp.MustCompile(`<title>(.*?) – QS-PodScript</title>`)

// serverEpisodeTitle reads an episode's title from its server page ("" if not possible).
func serverEpisodeTitle(ctx context.Context, c remoteConf, id int64) string {
	return serverPageTitle(ctx, c, fmt.Sprintf("/episodes/%d", id))
}

// serverPageTitle reads the title of one of the server's pages ("" if not possible).
func serverPageTitle(ctx context.Context, c remoteConf, path string) string {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.URL+path, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := apiClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if m := pageTitleRe.FindSubmatch(b); m != nil {
		return html.UnescapeString(string(m[1]))
	}
	return ""
}
