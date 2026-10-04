package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	st         *Store
	worker     *Worker
	pages      map[string]*template.Template
	serverMode bool   // qs-podscript server: logins, rights, public read-only pages
	localURL   string // local mode: own address, e.g. http://127.0.0.1:8321/
	proxyPort  int    // local mode: first port tried for the server pages
}

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8321, "port for the local web interface")
	noBrowser := fs.Bool("no-browser", false, "don't open the browser automatically")
	fs.Parse(args)

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if n, _ := st.ResetStuck(); n > 0 {
		logf("Requeued %d episode(s) that were interrupted last time", n)
	}

	s := &Server{st: st, worker: newWorker(st)}
	if err := s.loadTemplates(); err != nil {
		return err
	}

	// find a free port, starting at the requested one
	var ln net.Listener
	for p := *port; p < *port+20; p++ {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			break
		}
	}
	if ln == nil {
		return fmt.Errorf("no free port found near %d: %v", *port, err)
	}
	addr := "http://" + ln.Addr().String() + "/"
	s.localURL, s.proxyPort = addr, ln.Addr().(*net.TCPAddr).Port+2
	logf("QS-PodScript %s is running at %s", version, addr)
	if _, err := startPassThrough(st, s.localURL, s.proxyPort); err != nil {
		logf("warning: %v", err)
	}
	go runHelperPoller(ctx, st, s.worker)
	warnOtherInstall()
	logf("Keep this window open while you use it. Close it (or press Ctrl+C) to quit.")
	if !*noBrowser {
		openBrowser(addr)
	}
	go runSearchIndexer(ctx, st)
	go runArtwork(ctx, st)
	return s.serve(ctx, ln)
}

// cmdServer runs the shared homeserver: everybody can read, logged-in users
// edit what they have rights for. Meant to run behind a reverse proxy (HTTPS).
func cmdServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8322", "address to listen on (put a reverse proxy with HTTPS in front)")
	trusted := fs.String("trusted-proxy", "", "address(es) of the reverse proxy if it runs on another machine/container, e.g. 10.0.0.5 or 10.0.0.0/24 (comma-separated)")
	fs.Parse(args)
	if err := setTrustedProxies(*trusted); err != nil {
		return err
	}

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	if n, _ := st.ResetStuck(); n > 0 {
		logf("Requeued %d episode(s) that were interrupted last time", n)
	}
	s := &Server{st: st, worker: newWorker(st), serverMode: true}
	if err := s.loadTemplates(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	logf("QS-PodScript %s server is running at http://%s/", version, ln.Addr())
	if st.CountAdmins() == 0 {
		logf("NOTE: no admin yet. Create one with: qs-podscript user add <name> --admin")
	}
	go runSearchIndexer(ctx, st)
	go runArtwork(ctx, st)
	return s.serve(ctx, ln)
}

func (s *Server) serve(ctx context.Context, ln net.Listener) error {
	startGPUCheck()
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 20 * time.Second}
	go func() {
		<-ctx.Done()
		logf("Shutting down...")
		s.worker.Stop(true)
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	// give an aborted episode a moment to be put back into the queue
	for i := 0; i < 50 && s.worker.Busy(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		debugf("could not open browser: %v", err)
	}
}

// ------------------------------------------------------------ templates

var tmplFuncs = template.FuncMap{
	"versionOlder": func(a, b string) bool { return !versionAtLeast(a, b) },
	"date": func(ts int64) string {
		if ts == 0 {
			return "–"
		}
		return time.Unix(ts, 0).Format("2006-01-02")
	},
	"datetime":  func(ts int64) string { return time.Unix(ts, 0).Format("2006-01-02 15:04") },
	"clock":     fmtTime,
	"secs":      func(s float64) string { return fmtTime(int64(s * 1000)) },
	"label":     labelName,
	"queueWhy":  queueWhy,
	"queueRank": queueRank,
	"spClass":   spClass,
	"statusTxt": statusText,
	"isPerson":  func(l int) bool { _, ok := labelPerson(l); return ok },
	"roleText":  roleText,
	"markName":  markName,
	"ago": func(t int64) string {
		if t == 0 {
			return "never"
		}
		return time.Unix(t, 0).Format("2006-01-02 15:04")
	},
	"hours": func(sec int64) string {
		if sec < 3600 {
			return fmt.Sprintf("%d min", (sec+30)/60)
		}
		return fmt.Sprintf("%.1f h", float64(sec)/3600)
	},
	"lower": strings.ToLower,
	"role": func(m map[int64]string, id int64) string {
		return m[id]
	},
	"pctOf": func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return float64(a) * 100 / float64(b)
	},
	// barClass: width class p0..p100 for a progress bar (app.css) - no
	// inline style, so a strict Content-Security-Policy works
	"barClass": func(v any) string {
		var p float64
		switch x := v.(type) {
		case int:
			p = float64(x)
		case int64:
			p = float64(x)
		case float64:
			p = x
		}
		p = math.Round(p)
		if p < 0 {
			p = 0
		} else if p > 100 {
			p = 100
		}
		return fmt.Sprintf("p%d", int(p))
	},
	"json": func(v any) template.JS {
		b, _ := json.Marshal(v)
		return template.JS(b)
	},
}

func spClass(l int) string {
	if id, ok := labelPerson(l); ok {
		return fmt.Sprintf("sp%d person", id%8)
	}
	switch l {
	case labelUnknown:
		return "spu"
	case labelCrosstalk:
		return "spx"
	}
	// unnamed voices get muted colours (still distinguishable from each
	// other), named people the full colours + underlined name
	return fmt.Sprintf("spv spv%d", l%8)
}

func statusText(s string) string {
	switch s {
	case "new":
		return "Waiting"
	case "queued":
		return "Next up"
	case "processing":
		return "In progress"
	case "leased":
		return "Being transcribed by a helper"
	case "skipped":
		return "Not queued"
	case "done":
		return "Done"
	case "error":
		return "Failed"
	}
	return s
}

func (s *Server) loadTemplates() error {
	s.pages = map[string]*template.Template{}
	names, err := fs.Glob(webFS, "web/templates/*.html")
	if err != nil {
		return err
	}
	for _, n := range names {
		base := filepath.Base(n)
		if base == "layout.html" {
			continue
		}
		t, err := template.New("").Funcs(tmplFuncs).ParseFS(webFS, "web/templates/layout.html", n)
		if err != nil {
			return fmt.Errorf("template %s: %w", base, err)
		}
		s.pages[strings.TrimSuffix(base, ".html")] = t
	}
	return nil
}

type pageData struct {
	Title  string
	Nav    string
	Status Status
	Setup  SetupState
	Msg    string
	Err    string
	Data   any

	ServerMode    bool
	User          *User       // server mode: logged-in user or nil
	ViaLocal      string      // server mode, opened through a local app: link back to it
	LocalVersion  string      // via-local: the local app runs another version than this server
	ServerVersion string      // local app: the connected server runs another version
	Connected     bool        // local app with saved servers
	Places        []place     // the place menu in the header (local app, or server pages opened through it)
	PlaceName     string      // where you work right now
	OnServer      bool        // ... and that is a server
	ContentBase   string      // local app with a server active: its pages (Podcasts, Search, People) are here
	LocalBase     string      // link prefix for this computer's own pages ("" on local pages)
	SwitchURL     string      // where the place menu posts to
	ActiveServer  int64       // server pages through the local app: the id the local app gave this server
	ServerAdmin   bool        // local app with a server active: you are an admin there (menu shows Users)
	Look          string      // look.go
	Helpers       *helperWork // server that leaves the transcribing to helpers' computers
	Here          string      // this page's path (return address of forms)
	Meta          pageMeta    // meta.go: description, link preview, indexing
}

// IsAdmin / CanEdit decide what the page shows (local mode: everything).
func (p pageData) IsAdmin() bool { return !p.ServerMode || p.User.IsAdmin() }
func (p pageData) CanEdit(feedID int64) bool {
	return !p.ServerMode || p.User.IsAdmin() || (p.User != nil && p.User.Podcasts[feedID])
}
func (p pageData) LoggedIn() bool { return !p.ServerMode || p.User != nil }

// Version: for ?v= on the static files (cache busting after updates).
func (p pageData) Version() string { return version }

func (s *Server) render(w http.ResponseWriter, r *http.Request, page, title, nav string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, 500)
		return
	}
	pd := pageData{
		Title: title, Nav: nav, Status: getStatus(), Setup: getSetupState(s.st),
		Msg: r.URL.Query().Get("msg"), Err: r.URL.Query().Get("err"), Data: data,
		ServerMode: s.serverMode, User: currentUser(r), Here: r.URL.Path,
	}
	if !s.serverMode {
		pd.SwitchURL = "/switch"
		if ps := placeList(s.st); len(ps) > 1 {
			pd.Connected, pd.Places = true, ps
			pd.PlaceName = "This computer"
			if c, ok := activeServer(s.st); ok {
				pd.PlaceName, pd.OnServer = c.Label(), true
				pd.ContentBase = passThroughAddr()
				pd.ServerAdmin = c.IsAdmin()
				if sv := seenServerVersion(c); sv != "" && sv != version {
					pd.ServerVersion = sv
				}
			}
		}
	}
	if s.helpersOnly() {
		h := s.st.helperWork()
		pd.Helpers = &h
	}
	if s.serverMode && pd.User != nil && bearerToken(r) != "" {
		if l := r.Header.Get(localAppHeader); localBackLink.MatchString(l) {
			pd.ViaLocal = l
			pd.LocalBase = strings.TrimRight(l, "/")
			pd.SwitchURL = "/_local/switch"
			if v := r.Header.Get(apiVersionHdr); v != "" && v != version {
				pd.LocalVersion = v
			}
			if ps := decodePlaces(r.Header.Get(placesHeader)); len(ps) > 1 {
				pd.Places = ps
				for _, p := range ps {
					if p.Active {
						pd.PlaceName, pd.OnServer, pd.ActiveServer = p.Name, p.ID != 0, p.ID
					}
				}
			}
		}
	}
	pd.Look = s.lookFor(r, pd.ViaLocal != "")
	pd.Meta = s.buildMeta(r, page, title, pd.Look, data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", pd); err != nil {
		debugf("render %s: %v", page, err)
	}
}

// redirect after POST, carrying a short message
func back(w http.ResponseWriter, r *http.Request, to, msg, errMsg string) {
	q := url.Values{}
	if msg != "" {
		q.Set("msg", msg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	frag := ""
	if i := strings.Index(to, "#"); i >= 0 {
		to, frag = to[:i], to[i:] // the query must come before the #fragment
	}
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		to += sep + q.Encode()
	}
	http.Redirect(w, r, to+frag, http.StatusSeeOther)
}

// ------------------------------------------------------------ routes

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web/static")
	mux.Handle("GET /static/", staticFiles(http.StripPrefix("/static/", http.FileServer(http.FS(static)))))

	mux.HandleFunc("GET /{$}", s.guard(accessPublic, s.handleHome))
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /sitemap.xml", s.handleSitemap)
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
	mux.HandleFunc("GET /setup", s.guard(accessAdmin, s.handleSetupPage))
	mux.HandleFunc("POST /setup", s.guard(accessAdmin, s.handleSetupStart))
	mux.HandleFunc("POST /settings/speakers", s.guard(accessAdmin, s.handleSpeakerSettings))
	mux.HandleFunc("POST /settings/server-work", s.guard(accessAdmin, s.handleServerWork))
	mux.HandleFunc("POST /settings/transcription", s.guard(accessAdmin, s.handleTranscriptionSettings))
	mux.HandleFunc("POST /settings/look", s.guard(accessAdmin, s.handleLook))
	mux.HandleFunc("GET /feeds/new", s.guard(accessAdmin, s.handleFeedNew))
	mux.HandleFunc("POST /feeds", s.guard(accessAdmin, s.handleFeedCreate))
	mux.HandleFunc("GET /feeds/{id}", s.guard(accessPublic, s.handleFeed))
	mux.HandleFunc("POST /feeds/{id}/settings", s.guard(accessAdmin, s.handleFeedSettings))
	mux.HandleFunc("POST /feeds/{id}/refresh", s.guard(accessAdmin, s.handleFeedRefresh))
	mux.HandleFunc("POST /feeds/{id}/delete", s.guard(accessAdmin, s.handleFeedDelete))
	mux.HandleFunc("POST /feeds/{id}/first", s.guard(accessAdmin, s.handleFeedFirst))
	mux.HandleFunc("POST /queue/start", s.guard(accessAdmin, s.handleQueueStart))
	mux.HandleFunc("GET /queue", s.guard(accessAdmin, s.handleQueuePage))
	mux.HandleFunc("POST /queue/{id}/{how}", s.guard(accessAdmin, s.handleQueueMove))
	mux.HandleFunc("POST /queue/order", s.guard(accessAdmin, s.handleQueueOrder))
	mux.HandleFunc("POST /queue/stop", s.guard(accessAdmin, s.handleQueueStop))
	mux.HandleFunc("GET /episodes/{id}", s.guard(accessPublic, s.handleEpisode))
	mux.HandleFunc("GET /episodes/{id}/transcript.txt", s.guard(accessPublic, s.handleTranscriptTxt))
	mux.HandleFunc("POST /episodes/{id}/process", s.guard(accessAdmin, s.handleEpisodeProcess))
	mux.HandleFunc("POST /episodes/{id}/activate", s.guard(accessAdmin, s.handleEpisodeActivate))
	mux.HandleFunc("POST /episodes/{id}/rediarize", s.guard(accessAdmin, s.handleEpisodeRediarize))
	mux.HandleFunc("POST /episodes/{id}/people", s.guard(accessEdit, s.handleEpisodePeople))
	mux.HandleFunc("POST /versions/{vid}/corrections", s.guard(accessEdit, s.handleCorrectionAdd))
	mux.HandleFunc("POST /versions/{vid}/check", s.guard(accessEdit, s.handleQuizAnswer))
	mux.HandleFunc("GET /feeds/{id}/quiz", s.guard(accessEdit, s.handleQuiz))
	mux.HandleFunc("GET /episodes/{id}/quiz", s.guard(accessEdit, s.handleQuiz))
	mux.HandleFunc("GET /episodes/{id}/voice/{label}", s.guard(accessEdit, s.handleVoicePage))
	mux.HandleFunc("POST /versions/{vid}/voicemerge", s.guard(accessEdit, s.handleVoiceMerge))
	mux.HandleFunc("POST /versions/{vid}/identify", s.guard(accessEdit, s.handleIdentify))
	mux.HandleFunc("GET /people", s.guard(accessUser, s.handlePeople))
	mux.HandleFunc("POST /people", s.guard(accessAdmin, s.handlePersonAdd))
	mux.HandleFunc("GET /people/{id}", s.guard(accessUser, s.handlePerson))
	mux.HandleFunc("POST /people/{id}/rename", s.guard(accessAdmin, s.handlePersonRename))
	mux.HandleFunc("POST /people/{id}/delete", s.guard(accessAdmin, s.handlePersonDelete))
	mux.HandleFunc("POST /samples/{sid}/delete", s.guard(accessAdmin, s.handleSampleDelete))
	mux.HandleFunc("POST /feeds/{id}/roster", s.guard(accessEdit, s.handleRoster))
	mux.HandleFunc("POST /feeds/{id}/sources", s.guard(accessAdmin, s.handleSourceAdd))
	mux.HandleFunc("POST /feeds/{id}/sources/{sid}/delete", s.guard(accessAdmin, s.handleSourceDelete))
	mux.HandleFunc("POST /feeds/{id}/roster/{pid}", s.guard(accessEdit, s.handleRole))
	mux.HandleFunc("POST /feeds/{id}/spelling", s.guard(accessEdit, s.handleSpellingAdd))
	mux.HandleFunc("POST /feeds/{id}/spelling/{sid}/delete", s.guard(accessEdit, s.handleSpellingDelete))
	mux.HandleFunc("POST /feeds/{id}/spelling/{sid}/scope", s.guard(accessEdit, s.handleSpellingScope))
	mux.HandleFunc("POST /feeds/{id}/verify", s.guard(accessAdmin, s.handleVerify))
	mux.HandleFunc("POST /versions/{vid}/corrections/{cid}/delete", s.guard(accessEdit, s.handleCorrectionDelete))
	mux.HandleFunc("POST /versions/{vid}/carried/delete", s.guard(accessEdit, s.handleCarriedDelete))
	mux.HandleFunc("GET /audio/{file}", s.guard(accessPublic, s.handleAudio))
	mux.HandleFunc("GET /events", s.guard(accessAdmin, s.handleEvents))
	mux.HandleFunc("GET /log", s.guard(accessAdmin, s.handleLog))
	mux.HandleFunc("GET /export", s.guard(accessAdmin, s.handleExport))
	mux.HandleFunc("POST /import", s.guard(accessAdmin, s.handleImport))
	mux.HandleFunc("POST /import/cancel", s.guard(accessAdmin, s.handleImportCancel))
	mux.HandleFunc("GET /help", s.guard(accessPublic, s.handleHelp))
	mux.HandleFunc("GET /search", s.guard(accessPublic, s.handleSearch))
	mux.HandleFunc("GET /img/{file}", s.guard(accessPublic, s.handleImage))
	if !s.serverMode {
		mux.HandleFunc("POST /remote/connect", s.handleRemoteConnect)
		mux.HandleFunc("POST /remote/{sid}/disconnect", s.handleRemoteDisconnect)
		mux.HandleFunc("POST /remote/{sid}/rename", s.handleRemoteRename)
		mux.HandleFunc("POST /switch", s.handleSwitch)
		mux.HandleFunc("POST /queue/asks/drop", s.handleAskDrop)
		mux.HandleFunc("POST /remote/{sid}/work", s.handleRemoteWork)
	}
	if s.serverMode {
		mux.HandleFunc("GET /login", s.handleLoginPage)
		mux.HandleFunc("POST /login", s.handleLogin)
		mux.HandleFunc("POST /logout", s.handleLogout)
		mux.HandleFunc("GET /users", s.guard(accessAdmin, s.handleUsers))
		mux.HandleFunc("POST /users", s.guard(accessAdmin, s.handleUserAdd))
		mux.HandleFunc("POST /users/{id}", s.guard(accessAdmin, s.handleUserUpdate))
		mux.HandleFunc("POST /users/{id}/delete", s.guard(accessAdmin, s.handleUserDelete))
		mux.HandleFunc("GET /users/{id}", s.guard(accessAdmin, s.handleUserPage))
		mux.HandleFunc("GET /users/activity", s.guard(accessAdmin, s.handleActivity))
		mux.HandleFunc("GET /account", s.guard(accessUser, s.handleAccount))
		mux.HandleFunc("POST /account/password", s.guard(accessUser, s.handleOwnPassword))
		mux.HandleFunc("POST /account/name", s.guard(accessUser, s.handlePublicName))
		mux.HandleFunc("GET /work", s.guard(accessAdmin, s.handleWork))
		mux.HandleFunc("POST /work/{id}/release", s.guard(accessAdmin, s.handleWorkRelease))
		mux.HandleFunc("POST /keys/{tid}/delete", s.guard(accessUser, s.handleTokenDelete))
		mux.HandleFunc("POST /api/v1/login", s.handleAPILogin)
		mux.HandleFunc("GET /api/v1/podcasts", s.handleAPIPodcasts)
		mux.HandleFunc("GET /api/v1/search", s.handleAPISearch)
		mux.HandleFunc("POST /api/v1/work/claim", s.handleAPIClaim)
		mux.HandleFunc("POST /api/v1/work/{lease}/progress", s.handleAPIProgress)
		mux.HandleFunc("POST /api/v1/work/{lease}/fail", s.handleAPIFail)
		mux.HandleFunc("POST /api/v1/work/{lease}/result", s.handleAPIResult)
	}
	h := gzipResponses(sameOrigin(s.withUser(searchKicker(mux))))
	if !s.serverMode {
		return s.placeRedirect(h)
	}
	// helpers read this to notice when they run a different version
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(apiVersionHdr, version)
		h.ServeHTTP(w, r)
	})
}

// sameOrigin blocks POSTs coming from other websites (the server only listens
// on localhost, but a web page could still try to submit forms to it).
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// behind the reverse proxy the page is https, the app sees http
			okOrigin := func(o string) bool { return o == "http://"+r.Host || o == "https://"+r.Host }
			if o := r.Header.Get("Origin"); o != "" && !okOrigin(o) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if ref := r.Header.Get("Referer"); ref != "" && !strings.HasPrefix(ref, "http://"+r.Host+"/") && !strings.HasPrefix(ref, "https://"+r.Host+"/") {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ------------------------------------------------------------ home

type feedSummary struct {
	Feed
	Total, Done, Waiting, Failed int
}

func (s *Server) feedSummaries() []feedSummary {
	feeds, _ := s.st.Feeds()
	var out []feedSummary
	for _, f := range feeds {
		c, _ := s.st.StatusCounts(f.ID)
		fs := feedSummary{Feed: f, Done: c["done"], Waiting: c["new"] + c["queued"] + c["processing"], Failed: c["error"]}
		for _, n := range c {
			fs.Total += n
		}
		out = append(out, fs)
	}
	return out
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	log := recentLog()
	if len(log) > 12 {
		log = log[len(log)-12:]
	}
	data := map[string]any{
		"Feeds": s.feedSummaries(),
		"Log":   log,
	}
	if !s.serverMode {
		var list []remoteConf
		for _, c := range savedServers(s.st) {
			if c.Connected() {
				list = append(list, c)
			}
		}
		data["Servers"] = list
	}
	s.render(w, r, "home", "Overview", "home", data)
}

// ------------------------------------------------------------ setup

func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	type modelOpt struct {
		Key, Desc string
	}
	s.render(w, r, "setup", "Setup", "setup", map[string]any{
		"Models": []modelOpt{
			{"turbo", "Large v3 turbo – best choice with a graphics card (1.6 GB)"},
			{"turbo-q5", "Large v3 turbo, compressed – almost the same quality (550 MB)"},
			{"large-v3", "Large v3 – slowest, slightly more accurate (3 GB)"},
			{"medium.en", "Medium, English only (1.5 GB)"},
			{"small.en", "Small, English only – for computers without a graphics card (470 MB)"},
			{"base.en", "Base, English only – very fast, for testing only (140 MB)"},
		},
		"Current":         s.st.Setting("whisper_model", "turbo"),
		"GPUMode":         s.st.Setting("gpu_mode", gpuAuto),
		"Windows":         runtime.GOOS == "windows",
		"Mac":             runtime.GOOS == "darwin",
		"Diarize":         diarizeOptsFrom(s.st, 0),
		"WhisperContext":  whisperContext(s.st),
		"WhisperChunks":   whisperChunked(s.st),
		"FlashAttn":       flashAttn(s.st),
		"VoiceMerge":      voiceMergeSimilarity(s.st),
		"PendingImport":   pendingImportInfo(),
		"Servers":         savedServers(s.st),
		"Active":          activePlace(s.st),
		"ServerPages":     passThroughAddr(),
		"ActiveServer":    activeServerForSetup(r, s.st),
		"DiarizeModels":   diarizeModelRows(s.st),
		"SelfTranscribes": s.st.Setting("server_transcribes", "0") == "1",
		"Device":          speakerDevice(),
		"DeviceNow":       deviceSummary(),
		"GPUInstalled":    gpuLibsInstalled(),
		"Steps": []modelOpt{
			{"0.1", "Precise – checks every second (slowest)"},
			{"0.25", "Balanced – every 2.5 seconds, about 2.5× faster"},
			{"0.5", "Fast – every 5 seconds, about 4× faster"},
		},
	})
}

func (s *Server) handleTranscriptionSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.FormValue("context") == "1"
	pieces := r.FormValue("pieces") == "1"
	s.st.SetSetting("whisper_context", map[bool]string{true: "1", false: "0"}[ctx])
	s.st.SetSetting("whisper_chunks", map[bool]string{true: "1", false: "0"}[pieces])
	logf("Transcription settings: in pieces=%v, options %s", pieces, strings.Join(whisperExtraArgs(s.st), " "))
	back(w, r, "/setup", "Transcription settings saved. They apply to episodes transcribed from now on.", "")
}

type diarizeModelRow struct {
	Key, Label, SpeedText string
	Threshold, Default    float32
	Downloaded            bool
	SizeMB                int
}

var diarizeModelSizeMB = map[string]int{"resnet34": 25, "resnet152": 76, "resnet221": 91, "resnet293": 109,
	"titanet": 38, "titanet-large": 97, "eres2net": 25, "campplus": 28, "campplus-3d": 28}

func diarizeModelRows(st *Store) []diarizeModelRow {
	var rows []diarizeModelRow
	for _, d := range diarizeModelList {
		sp := "same"
		switch {
		case d.Speed >= 1.5:
			sp = fmt.Sprintf("%.1f× longer", d.Speed)
		case d.Speed > 1.05:
			sp = "a bit longer"
		case d.Speed < 0.95:
			sp = fmt.Sprintf("%.0f%% faster", (1/d.Speed-1)*100)
		}
		rows = append(rows, diarizeModelRow{Key: d.Key, Label: d.Label, SpeedText: sp,
			Threshold: defaultThresholdFor(d.Key, st), Default: d.Threshold,
			Downloaded: fileExists(diarizeModelPath(d.Key)), SizeMB: diarizeModelSizeMB[d.Key]})
	}
	return rows
}

func (s *Server) handleSpeakerSettings(w http.ResponseWriter, r *http.Request) {
	model, step := r.FormValue("model"), r.FormValue("step")
	switch {
	case diarizeModels[model] == "":
		back(w, r, "/setup", "", "Choose a voice model.")
		return
	case step != "0.1" && step != "0.25" && step != "0.5":
		back(w, r, "/setup", "", "Choose a detection precision.")
		return
	}
	ths := map[string]float64{}
	for _, d := range diarizeModelList {
		v := strings.TrimSpace(strings.Replace(r.FormValue("threshold_"+d.Key), ",", ".", 1))
		if v == "" {
			continue
		}
		th, err := strconv.ParseFloat(v, 64)
		if err != nil || th < 0.05 || th > 1.5 {
			back(w, r, "/setup", "", "The threshold of "+d.Label+" must be a number between 0.05 and 1.5.")
			return
		}
		ths[d.Key] = th
	}
	vm, err := strconv.ParseFloat(strings.Replace(r.FormValue("voice_merge"), ",", ".", 1), 64)
	if err != nil || vm < 0 || vm >= 1 {
		back(w, r, "/setup", "", "Voice merging must be a number between 0 and 0.99 (0 = off).")
		return
	}
	s.st.SetSetting("diarize_model", model)
	s.st.SetSetting("diarize_step", step)
	for k, th := range ths {
		if float32(th) == diarizeModelByKey(k).Threshold && s.st.Setting(thresholdSetting(k), "") == "" {
			continue // untouched default: keep following the built-in default
		}
		s.st.SetSetting(thresholdSetting(k), strconv.FormatFloat(th, 'f', -1, 64))
	}
	s.st.SetSetting("voice_merge", strconv.FormatFloat(vm, 'f', -1, 64))
	dev := r.FormValue("device")
	if dev != deviceCPU {
		dev = deviceAuto
	}
	s.st.SetSetting("speaker_device", dev)
	setSpeakerDevice(dev)
	if dev == deviceAuto && fileExists(gpuCrashMarker()) {
		resetGPUCheck()
	}
	logf("Speaker detection settings: %s, runs on %s", diarizeOptsFrom(s.st, 0), providerText(providerNow()))
	msg := "Speaker detection settings saved. They apply to episodes transcribed from now on."
	if !fileExists(diarizeModelPath(model)) {
		msg += " The voice model is downloaded automatically before the next episode."
	}
	back(w, r, "/setup", msg, "")
}

func (s *Server) handleSetupStart(w http.ResponseWriter, r *http.Request) {
	model := r.FormValue("model")
	if _, ok := whisperModels[model]; !ok {
		back(w, r, "/setup", "", "Choose a speech model.")
		return
	}
	mode := r.FormValue("gpu")
	if !validGPUMode(mode) {
		mode = gpuAuto
	}
	if err := s.worker.StartSetup(model, mode, r.FormValue("force") == "1"); err != nil {
		back(w, r, "/setup", "", err.Error())
		return
	}
	back(w, r, "/setup", "Installation started. Progress is shown at the top.", "")
}

// ------------------------------------------------------------ feeds

func (s *Server) handleFeedNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "feed_new", "Add a podcast", "feeds", nil)
}

func (s *Server) handleFeedCreate(w http.ResponseWriter, r *http.Request) {
	f := Feed{
		URL:          strings.TrimSpace(r.FormValue("url")),
		Language:     strings.TrimSpace(r.FormValue("lang")),
		SpeakerNames: cleanNames(r.FormValue("names")),
	}
	f.NumSpeakers, _ = strconv.Atoi(r.FormValue("speakers"))
	if f.Language == "" {
		f.Language = "auto"
	}
	if !strings.HasPrefix(f.URL, "http://") && !strings.HasPrefix(f.URL, "https://") {
		back(w, r, "/feeds/new", "", "The feed address must start with http:// or https://")
		return
	}
	title, items, err := fetchFeed(r.Context(), f.URL)
	if err != nil {
		back(w, r, "/feeds/new", "", "Could not read the feed: "+err.Error())
		return
	}
	f.Title = title
	id, err := s.st.AddFeed(f)
	if err != nil {
		if errors.Is(err, errDuplicateFeed) || strings.Contains(err.Error(), "UNIQUE") {
			back(w, r, "/feeds/new", "", "This podcast has already been added.")
			return
		}
		back(w, r, "/feeds/new", "", err.Error())
		return
	}
	var n int
	if srcs, _ := s.st.Sources(id); len(srcs) > 0 {
		n, err = s.st.UpsertEpisodes(id, srcs[0].ID, items)
	}
	if err != nil {
		back(w, r, "/feeds/new", "", err.Error())
		return
	}
	s.st.UpdateFeedMeta(id, title)
	if f.SpeakerNames != "" { // names given when adding -> people + hosts
		roles := map[int64]string{}
		for _, name := range strings.Split(f.SpeakerNames, ",") {
			if pid, err := s.st.AddPerson(name); err == nil {
				roles[pid] = roleHost
			}
		}
		s.st.SetRoster(id, roles)
		refreshPeopleCache(s.st)
	}
	logf("Added feed %d: %s (%d episodes)", id, title, n)
	back(w, r, fmt.Sprintf("/feeds/%d", id), fmt.Sprintf("Added %s with %d episodes.", title, n), "")
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := s.st.Feed(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	filter := r.URL.Query().Get("show")
	eps, err := s.st.Episodes(id, "", 0)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// visitors (server, no edit rights) only see "transcribed" / "all" - the
	// queue states are of no interest to them
	public := s.serverMode && !s.canEdit(r, id)
	if public && filter == "" {
		filter = "done"
	}
	counts := map[string]int{"all": len(eps)}
	var shown []Episode
	for _, e := range eps {
		group := episodeGroup(e.Status)
		if public {
			group = map[bool]string{true: "done", false: "notyet"}[e.ActiveVersionID.Valid]
		}
		counts[group]++
		if filter == "" || filter == "all" || filter == group {
			shown = append(shown, e)
		}
	}
	// newest first reads better in a list; processing order is still oldest first
	sort.SliceStable(shown, func(i, j int) bool { return shown[i].PubDate > shown[j].PubDate })
	if filter == "" {
		filter = "all"
	}
	roster, _ := s.st.Roster(id)
	people, _ := s.st.People()
	srcs, _ := s.st.Sources(id)
	srcNames := map[int64]string{} // only needed when there is more than one feed
	if len(srcs) > 1 {
		for _, src := range srcs {
			n := src.Title
			if n == "" {
				n = src.URL
			}
			srcNames[src.ID] = n
		}
	}
	s.render(w, r, "feed", f.Title, "feeds", map[string]any{
		"Feed": f, "Episodes": shown, "Counts": counts, "Filter": filter, "Public": public,
		"Roster": roster, "People": people, "RosterGroups": rosterGroups(people, roster),
		"Sources": srcs, "SourceNames": srcNames, "Quiz": s.st.feedQuizProgress(f.ID), "First": s.st.feedIsFirst(f.ID),
		"Spelling":  func() []SpellingRule { r, _ := s.st.SpellingRules(id); return r }(),
		"NameRules": nameRules(people),
	})
}

type rosterGroup struct {
	Role, Title, Empty string
	People             []Person
}

// rosterGroups: hosts, regulars, everybody else - each alphabetical.
func rosterGroups(people []Person, roster map[int64]string) []rosterGroup {
	gs := []rosterGroup{
		{Role: roleHost, Title: "Hosts", Empty: "No hosts yet."},
		{Role: rolePool, Title: "Regulars", Empty: "No regulars yet."},
		{Role: "", Title: "Not on this podcast", Empty: "Everybody known is on this podcast."},
	}
	for _, p := range people { // People() is sorted by name already
		switch roster[p.ID] {
		case roleHost:
			gs[0].People = append(gs[0].People, p)
		case rolePool:
			gs[1].People = append(gs[1].People, p)
		default:
			gs[2].People = append(gs[2].People, p)
		}
	}
	return gs
}

// ------------------------------------------------------------ connect to a server

var localBackLink = regexp.MustCompile(`^http://127\.0\.0\.1:\d{2,5}/$`)

// ------------------------------------------------------------ export / import

func pendingImportInfo() *exportInfo {
	if info, ok := pendingImport(); ok {
		return &info
	}
	return nil
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	name := "qs-podscript-export-" + time.Now().Format("2006-01-02") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := exportTo(w, s.st); err != nil {
		// headers are sent already - the download ends broken, the log says why
		logf("Export failed: %v", err)
		return
	}
	logf("Exported database and audio (%s)", name)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	to := "/setup#move"
	mr, err := r.MultipartReader()
	if err != nil {
		back(w, r, to, "", "Choose an export file first.")
		return
	}
	upload := filepath.Join(P.Work, "import-upload.zip")
	defer os.Remove(upload)
	confirmed, got := false, false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			back(w, r, to, "", "Upload failed: "+err.Error())
			return
		}
		switch part.FormName() {
		case "confirm":
			b, _ := io.ReadAll(io.LimitReader(part, 16))
			confirmed = string(b) == "1"
		case "file":
			f, err := os.Create(upload)
			if err != nil {
				back(w, r, to, "", err.Error())
				return
			}
			n, err := io.Copy(f, part) // streamed to disk: exports can be several GB
			f.Close()
			if err != nil {
				back(w, r, to, "", "Upload failed: "+err.Error())
				return
			}
			got = n > 0
		}
		part.Close()
	}
	if !got {
		back(w, r, to, "", "Choose an export file first.")
		return
	}
	if !confirmed {
		back(w, r, to, "", "Tick the box to confirm that the data on this computer should be replaced.")
		return
	}
	info, err := stageImport(upload)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	logf("Import prepared: %d podcasts, %d transcribed episodes from QS-PodScript %s - applied at the next start", info.Podcasts, info.Episodes, info.Version)
	back(w, r, to, "Import prepared. Close QS-PodScript and start it again to finish – until then nothing has changed.", "")
}

func (s *Server) handleImportCancel(w http.ResponseWriter, r *http.Request) {
	if err := cancelImport(); err != nil {
		back(w, r, "/setup#move", "", err.Error())
		return
	}
	back(w, r, "/setup#move", "Import cancelled. Nothing was changed.", "")
}

// ------------------------------------------------------------ feeds of a podcast

func (s *Server) handleSourceAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/feeds/%d#feeds", id)
	f, err := s.st.Feed(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u := strings.TrimSpace(r.FormValue("url"))
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		back(w, r, to, "", "The feed address must start with http:// or https://")
		return
	}
	if s.st.SourceExists(u) {
		back(w, r, to, "", "This feed is already used by a podcast.")
		return
	}
	title, items, err := fetchFeed(r.Context(), u)
	if err != nil {
		back(w, r, to, "", "Could not read the feed: "+err.Error())
		return
	}
	sid, err := s.st.AddSource(id, u, title)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	s.st.setSourceResult(sid, title, nil)
	n, err := s.st.UpsertEpisodes(id, sid, items)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	logf("Podcast %d (%s): added feed %q (%d episodes, %d new)", id, f.Title, title, len(items), n)
	back(w, r, to, fmt.Sprintf("Feed \u201c%s\u201d added: %d episodes, %d of them new to this podcast (the others were already there).", title, len(items), n), "")
}

func (s *Server) handleSourceDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/feeds/%d#feeds", id)
	sid, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	n, err := s.st.RemoveSource(id, sid)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, fmt.Sprintf("Feed removed, with %d episodes that were only in it and not transcribed. Transcribed episodes were kept.", n), "")
}

// ------------------------------------------------------------ spelling fixes

func (s *Server) handleSpellingAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/feeds/%d#spelling", id)
	rule := SpellingRule{
		FeedID:   id,
		Correct:  strings.Join(strings.Fields(r.FormValue("correct")), " "),
		Variants: splitVariants(r.FormValue("variants")),
	}
	if r.FormValue("all") == "1" {
		rule.FeedID = 0
	}
	if rule.Correct == "" || len(rule.Variants) == 0 {
		back(w, r, to, "", "Enter the correct spelling and at least one way it gets written wrong.")
		return
	}
	if len(rule.Correct) > 200 || len(rule.Variants) > 50 {
		back(w, r, to, "", "That's too long for a spelling fix.")
		return
	}
	if err := s.st.AddSpellingRule(rule); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, fmt.Sprintf("Spelling fix for \u201c%s\u201d saved. It applies to all transcripts right away.", rule.Correct), "")
}

func (s *Server) handleSpellingScope(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sid, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	to := fmt.Sprintf("/feeds/%d#spelling", id)
	scope, msg := id, "now only used for this podcast"
	if r.FormValue("all") == "1" {
		scope, msg = 0, "now used for all podcasts"
	}
	err := s.st.SetSpellingScope(sid, scope)
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		// the switch on the page calls this without reloading
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"all": scope == 0})
		return
	}
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Spelling fix "+msg+".", "")
}

func (s *Server) handleSpellingDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sid, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	to := fmt.Sprintf("/feeds/%d#spelling", id)
	if err := s.st.DeleteSpellingRule(sid); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Spelling fix removed.", "")
}

func episodeGroup(status string) string {
	switch status {
	case "done":
		return "done"
	case "error":
		return "failed"
	case "skipped":
		return "skipped"
	}
	return "waiting"
}

func (s *Server) handleFeedSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := s.st.Feed(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f.Language = strings.TrimSpace(r.FormValue("lang"))
	if f.Language == "" {
		f.Language = "auto"
	}
	f.NumSpeakers, _ = strconv.Atoi(r.FormValue("speakers"))
	if f.NumSpeakers < 0 {
		f.NumSpeakers = 0
	}
	f.VAD = r.FormValue("vad") == "1"
	if err := s.st.UpdateFeedSettings(f); err != nil {
		back(w, r, fmt.Sprintf("/feeds/%d", id), "", err.Error())
		return
	}
	back(w, r, fmt.Sprintf("/feeds/%d", id), "Settings saved. They apply to episodes transcribed from now on.", "")
}

func (s *Server) handleFeedRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := s.st.Feed(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	n, problems := refreshPodcast(r.Context(), s.st, f)
	if len(problems) > 0 {
		errMsg := "Could not read: " + strings.Join(problems, "; ")
		back(w, r, fmt.Sprintf("/feeds/%d", id), fmt.Sprintf("%d new episodes from the other feeds.", n), errMsg)
		return
	}
	msg := "No new episodes."
	if n == 1 {
		msg = "1 new episode."
	} else if n > 1 {
		msg = fmt.Sprintf("%d new episodes.", n)
	}
	back(w, r, fmt.Sprintf("/feeds/%d", id), msg, "")
}

// ------------------------------------------------------------ queue

func returnTo(r *http.Request, def string) string {
	if to := r.FormValue("return"); strings.HasPrefix(to, "/") && !strings.HasPrefix(to, "//") {
		return to
	}
	return def
}

func (s *Server) handleQueueStart(w http.ResponseWriter, r *http.Request) {
	to := returnTo(r, "/")
	feedID, _ := strconv.ParseInt(r.FormValue("feed"), 10, 64)
	if s.helpersOnly() {
		if feedID > 0 { // this podcast first
			s.st.putFeedFirst(feedID)
		}
		h := s.st.helperWork()
		if feedID > 0 {
			var n int
			s.st.db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE feed_id=? AND status IN ('new','queued')`, feedID).Scan(&n)
			switch r.Header.Get(localIdleHeader) { // clicked in a helper's QS-PodScript
			case "1":
				back(w, r, to, fmt.Sprintf("Your computer works through this podcast's %d waiting episode(s) now – its progress is shown at the top right.", n), "")
				return
			case "0":
				back(w, r, to, fmt.Sprintf("Your computer is busy right now – this podcast (%d waiting episode(s)) is on its list and is done right after the current work.", n), "")
				return
			}
			back(w, r, to, fmt.Sprintf("This podcast's %d waiting episode(s) come first for the helpers' computers (“Transcribe for this server” in their QS-PodScript, or “Transcribe this podcast” clicked there).", n), "")
			return
		}
		back(w, r, to, fmt.Sprintf("This server doesn't transcribe itself: %d episode(s) are waiting for the helpers' computers (“Transcribe for this server” in their QS-PodScript).", h.Waiting), "")
		return
	}
	if !getSetupState(s.st).Ready() {
		back(w, r, "/setup", "", "Finish the setup first.")
		return
	}
	err := s.worker.StartQueue(queueOpts{FeedID: feedID, Refresh: true, RetryErrors: r.FormValue("retry") == "1"})
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Transcribing started.", "")
}

func (s *Server) handleQueueStop(w http.ResponseWriter, r *http.Request) {
	now := r.FormValue("now") == "1"
	s.worker.Stop(now)
	msg := "Stopping after the current episode."
	if now {
		msg = "Stopped. The current episode goes back into the queue."
	}
	back(w, r, returnTo(r, "/"), msg, "")
}

// ------------------------------------------------------------ episodes

func (s *Server) handleEpisodeProcess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := returnTo(r, fmt.Sprintf("/episodes/%d", id))
	if s.helpersOnly() {
		if err := s.st.QueueEpisode(id); err != nil {
			back(w, r, to, "", err.Error())
			return
		}
		back(w, r, to, helperQueuedMsg(r, "transcribes this episode"), "")
		return
	}
	if !getSetupState(s.st).Ready() {
		back(w, r, "/setup", "", "Finish the setup first.")
		return
	}
	if err := s.st.QueueEpisode(id); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	if !s.worker.Busy() {
		// only this episode: limit 1 picks the highest priority one
		if err := s.worker.StartQueue(queueOpts{Limit: 1}); err != nil {
			back(w, r, to, "", err.Error())
			return
		}
		back(w, r, to, "Transcribing this episode now.", "")
		return
	}
	s.st.addAsk(ask{ID: id})
	back(w, r, to, "Your computer is busy – this episode is done right after the current work (or first thing when you press “Start transcribing”).", "")
}

func (s *Server) handleEpisodeRediarize(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/episodes/%d", id)
	vid, _ := strconv.ParseInt(r.FormValue("v"), 10, 64)
	src, err := s.st.Version(vid)
	if err != nil || src.EpisodeID != id {
		back(w, r, to, "", "Version not found.")
		return
	}
	if s.helpersOnly() {
		if src.AudioFile == "" || !fileExists(filepath.Join(P.Audio, src.AudioFile)) {
			back(w, r, to, "", "This version has no saved audio copy – use “Transcribe again” instead.")
			return
		}
		if err := s.st.QueueRediarize(id, src.ID); err != nil {
			back(w, r, to, "", err.Error())
			return
		}
		back(w, r, to, helperQueuedMsg(r, "redoes the speaker detection")+" The result appears as a new version.", "")
		return
	}
	if s.worker.Busy() {
		if src.AudioFile == "" || !fileExists(filepath.Join(P.Audio, src.AudioFile)) {
			back(w, r, to, "", "This version has no saved audio copy – use “Transcribe again” instead.")
			return
		}
		if err := s.st.QueueRediarize(id, src.ID); err != nil {
			back(w, r, to, "", err.Error())
			return
		}
		s.st.addAsk(ask{ID: id, Kind: "diarize"})
		back(w, r, to, "Your computer is busy – the speaker detection is redone right after the current work (or first thing when you press “Start transcribing”). The result appears as a new version.", "")
		return
	}
	if err := s.worker.StartRediarize(src); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Redoing speaker detection with the current settings. The result appears as a new version.", "")
}

func (s *Server) handleEpisodeActivate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	vid, _ := strconv.ParseInt(r.FormValue("v"), 10, 64)
	if err := s.st.SetActiveVersion(id, vid); err != nil {
		back(w, r, fmt.Sprintf("/episodes/%d", id), "", err.Error())
		return
	}
	back(w, r, fmt.Sprintf("/episodes/%d", id), fmt.Sprintf("Version %d is now the main version.", vid), "")
}

type lane struct {
	Label   int
	Share   int // percent of speaking time
	Blocks  []laneBlock
	TotalMs int64
}

type laneBlock struct{ Left, Width float64 }

// buildLanes shows who speaks when: one lane per speaker, ordered by speaking
// time, built from the final (corrected) utterances so it matches the text.
func buildLanes(us []Utterance, totalMs int64) []lane {
	if totalMs <= 0 && len(us) > 0 {
		totalMs = us[len(us)-1].EndMs
	}
	if totalMs <= 0 {
		return nil
	}
	byLabel := map[int]*lane{}
	var all int64
	for _, u := range us {
		l := byLabel[u.Label]
		if l == nil {
			l = &lane{Label: u.Label}
			byLabel[u.Label] = l
		}
		d := max64(u.EndMs-u.StartMs, 0)
		l.TotalMs += d
		all += d
		l.Blocks = append(l.Blocks, laneBlock{
			Left:  float64(u.StartMs) * 100 / float64(totalMs),
			Width: max(float64(d)*100/float64(totalMs), 0.15),
		})
	}
	var out []lane
	for _, l := range byLabel {
		if all > 0 {
			l.Share = int((l.TotalMs*1000/all + 5) / 10)
		}
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalMs > out[j].TotalMs })
	return out
}

// nextLabel returns an unused speaker number for "new speaker".
func nextLabel(turns []Turn, corr []Correction) int {
	n := 0
	for _, t := range turns {
		n = max(n, t.Cluster+1)
	}
	for _, c := range corr {
		for _, l := range []int{c.Label, c.From} {
			if l >= 0 && l < personLabelBase {
				n = max(n, l+1)
			}
		}
	}
	return n
}

func (s *Server) episodeVersion(r *http.Request, ep Episode) (int64, error) {
	if v := r.URL.Query().Get("v"); v != "" {
		vid, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, err
		}
		ver, err := s.st.Version(vid)
		if err != nil || ver.EpisodeID != ep.ID {
			return 0, fmt.Errorf("version not found")
		}
		return vid, nil
	}
	if ep.ActiveVersionID.Valid {
		return ep.ActiveVersionID.Int64, nil
	}
	return 0, nil
}

func (s *Server) handleEpisode(w http.ResponseWriter, r *http.Request) {
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
	feed, _ := s.st.Feed(ep.FeedID)
	versions, _ := s.st.Versions(id)
	vid, err := s.episodeVersion(r, ep)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Episode": ep, "Feed": feed, "Versions": versions, "VersionID": vid}
	data["EpisodePeople"] = s.st.episodePeopleView(ep)
	if kind, _ := s.st.episodeJob(ep.ID); kind == jobDiarize && (ep.Status == "queued" || ep.Status == "new" || ep.Status == "leased") {
		data["PendingJob"] = "speaker detection"
	} else if s.helpersOnly() && ep.ActiveVersionID.Valid && (ep.Status == "queued" || ep.Status == "leased") {
		data["PendingJob"] = "transcription"
	}
	if vid > 0 {
		ver, _ := s.st.Version(vid)
		segs, toks, turns, err := s.st.LoadResults(vid)
		corr, _ := s.st.Corrections(vid)
		if err == nil {
			us := applySpelling(buildUtterances(segs, toks, turns, corr), spellingFor(s.st, ep.FeedID))
			lanes := buildLanes(us, int64(ver.AudioSeconds*1000))
			var speakers []lane // selectable speakers: real ones, by number
			for _, l := range lanes {
				if l.Label >= 0 {
					speakers = append(speakers, l)
				}
			}
			sort.Slice(speakers, func(i, j int) bool { return speakers[i].Label < speakers[j].Label })
			data["Version"] = ver
			if s.serverMode {
				real := currentUser(r).IsAdmin()
				if ver.TranscribedBy != 0 {
					data["TranscribedBy"] = s.st.creditName(ver.TranscribedBy, real)
				}
				data["Cleanup"] = s.st.episodeCredits(ep.ID, real)
			}
			markSearchHits(us, r.URL.Query().Get("hl"))
			data["Utterances"] = us
			if ver.Status == "done" && ep.ActiveVersionID.Valid && ep.ActiveVersionID.Int64 == vid {
				ivs := s.st.checkedIntervals(vid, corr)
				total, done := quizProgress(quizWindows(quizRows(us)), ivs)
				s.st.saveQuizProgress(vid, total, done)
				data["QuizPercent"] = percent(done, total)
				data["QuizOpen"] = total > 0
				checked := map[int64]string{}
				for _, u := range us {
					if st := checkState(ivs, u.StartMs, u.EndMs); st != "" && u.Mark == "" {
						checked[u.StartMs] = st
					}
				}
				data["CheckedAt"] = checked
				data["HasChecked"] = len(ivs) > 0
				if l := checkedLane(ivs, us, int64(ver.AudioSeconds*1000)); l != nil && len(l.Blocks) > 0 {
					data["CheckedLane"] = l
				}
			}
			data["Loops"] = findLoops(us)
			data["Lanes"] = lanes
			data["MarkLanes"] = buildMarkLanes(us, int64(ver.AudioSeconds*1000))
			marked := map[string]bool{}
			for _, u := range us {
				if u.Mark != "" {
					marked[u.Mark] = true
				}
			}
			data["HasAds"], data["HasClips"] = marked[markAd], marked[markClip]
			data["Speakers"] = speakers
			data["Corrections"] = corr
			data["People"] = s.st.PersonOptions(ep.FeedID)
			data["ChipPeople"], _ = s.st.chipPeople(ep.FeedID, ep.ID)
			var autoRanges, manualCorr []Correction
			carried := 0
			for _, c := range corr {
				switch {
				case c.Auto && c.Kind == "range":
					autoRanges = append(autoRanges, c)
				case c.Origin == originCarried:
					carried++
				default:
					manualCorr = append(manualCorr, c)
				}
			}
			data["AutoIdentified"] = len(autoRanges)
			data["ListCorrections"] = manualCorr
			data["Carried"] = carried
			data["TotalMs"] = int64(ver.AudioSeconds * 1000)
			if ver.AudioFile != "" && fileExists(filepath.Join(P.Audio, ver.AudioFile)) {
				data["Audio"] = "/audio/" + ver.AudioFile
			}
		}
	}
	s.render(w, r, "episode", ep.Title, "feeds", data)
}

func (s *Server) handleTranscriptTxt(w http.ResponseWriter, r *http.Request) {
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
	vid, err := s.episodeVersion(r, ep)
	if err != nil || vid == 0 {
		http.NotFound(w, r)
		return
	}
	segs, toks, turns, err := s.st.LoadResults(vid)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	corr, _ := s.st.Corrections(vid)
	noAds := r.URL.Query().Get("ads") == "0"
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\r\n\r\n", ep.Title)
	for _, u := range applySpelling(buildUtterances(segs, toks, turns, corr), spellingFor(s.st, ep.FeedID)) {
		if noAds && u.Mark == markAd {
			continue
		}
		tag := ""
		if u.Mark != "" {
			tag = " [" + markName(u.Mark) + "]"
		}
		fmt.Fprintf(&sb, "[%s]%s %s: %s\r\n\r\n", fmtTime(u.StartMs), tag, labelName(u.Label), u.Text)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="episode-%d.txt"`, id))
	w.Write([]byte(sb.String()))
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".ogg") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/ogg")
	http.ServeFile(w, r, filepath.Join(P.Audio, name)) // supports seeking (Range requests)
}

// ------------------------------------------------------------ live updates + log

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, unsub := subscribeStatus()
	defer unsub()

	wantLog := r.URL.Query().Get("log") == "1"
	logSeen := logSeq()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case st := <-ch:
			b, _ := json.Marshal(st)
			fmt.Fprintf(w, "event: status\ndata: %s\n\n", b)
			fl.Flush()
		case <-tick.C:
			if wantLog {
				lines, seq := logSince(logSeen)
				logSeen = seq
				for _, l := range lines {
					fmt.Fprintf(w, "event: log\ndata: %s\n\n", strings.ReplaceAll(l, "\n", " "))
				}
			}
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "help", "Manual", "help", map[string]any{"Tools": toolVersions(s.st)})
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Lines": recentLog(), "Path": P.Log}
	if !s.serverMode {
		data["ActiveServer"] = activeServerForSetup(r, s.st)
	}
	s.render(w, r, "log", "Activity log", "log", data)
}

// ------------------------------------------------------------ corrections

func (s *Server) correctionVersion(w http.ResponseWriter, r *http.Request) (Version, bool) {
	vid, err := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return Version{}, false
	}
	v, err := s.st.Version(vid)
	if err != nil || v.Status != "done" {
		http.NotFound(w, r)
		return Version{}, false
	}
	return v, true
}

func (s *Server) handleCorrectionAdd(w http.ResponseWriter, r *http.Request) {
	v, ok := s.correctionVersion(w, r)
	if !ok {
		return
	}
	to := fmt.Sprintf("/episodes/%d?v=%d", v.EpisodeID, v.ID)
	fail := func(msg string) { back(w, r, to, "", msg) }

	if r.FormValue("kind") == "mark" {
		c := Correction{Kind: "mark", Text: r.FormValue("mark"), UserID: s.userID(r)}
		c.StartMs, _ = strconv.ParseInt(r.FormValue("start"), 10, 64)
		c.EndMs, _ = strconv.ParseInt(r.FormValue("end"), 10, 64)
		if c.EndMs <= c.StartMs || c.StartMs < 0 {
			fail("Select some text first.")
			return
		}
		if !validMark(c.Text) {
			fail("Unknown mark.")
			return
		}
		if _, err := s.st.AddCorrectionID(v.ID, c); err != nil {
			fail(err.Error())
			return
		}
		if c.Text == markClip {
			// voice samples taken from this passage earlier would teach actor voices
			if n, err := s.st.DeleteVoiceSamplesIn(v.ID, c.StartMs, c.EndMs); err == nil && n > 0 {
				logf("Episode %d v%d: removed %d voice samples inside a movie clip", v.EpisodeID, v.ID, n)
			}
		}
		http.Redirect(w, r, fmt.Sprintf("%s#at%d", to, c.StartMs), http.StatusSeeOther)
		return
	}

	if r.FormValue("kind") == "text" {
		c := Correction{Kind: "text", Text: strings.Join(strings.Fields(r.FormValue("text")), " "), UserID: s.userID(r)}
		c.StartMs, _ = strconv.ParseInt(r.FormValue("start"), 10, 64)
		c.EndMs, _ = strconv.ParseInt(r.FormValue("end"), 10, 64)
		if c.EndMs <= c.StartMs || c.StartMs < 0 {
			fail("Select some text first.")
			return
		}
		if _, err := s.st.AddCorrectionID(v.ID, c); err != nil {
			fail(err.Error())
			return
		}
		http.Redirect(w, r, fmt.Sprintf("%s#at%d", to, c.StartMs), http.StatusSeeOther)
		return
	}

	_, _, turns, err := s.st.LoadResults(v.ID)
	if err != nil {
		fail(err.Error())
		return
	}
	corr, _ := s.st.Corrections(v.ID)
	c := Correction{Kind: r.FormValue("kind"), UserID: s.userID(r)}
	var personID int64
	switch lv := r.FormValue("label"); {
	case lv == "newperson":
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			fail("Enter a name for the new person.")
			return
		}
		personID, err = s.st.AddPerson(name)
		if err != nil {
			fail(err.Error())
			return
		}
		refreshPeopleCache(s.st)
		c.Label = personLabel(personID)
	case strings.HasPrefix(lv, "p"):
		personID, err = strconv.ParseInt(lv[1:], 10, 64)
		if err != nil || personID <= 0 {
			fail("Choose a speaker.")
			return
		}
		c.Label = personLabel(personID)
	}
	switch lv := r.FormValue("label"); {
	case personID > 0:
		// already set
	case lv == "new":
		c.Label = nextLabel(turns, corr)
	case lv == "x":
		c.Label = labelCrosstalk
	case lv == "u":
		c.Label = labelUnknown
	default:
		c.Label, err = strconv.Atoi(lv)
		if err != nil || c.Label < 0 {
			fail("Choose a speaker.")
			return
		}
	}
	anchor := int64(0)
	switch c.Kind {
	case "range":
		c.StartMs, _ = strconv.ParseInt(r.FormValue("start"), 10, 64)
		c.EndMs, _ = strconv.ParseInt(r.FormValue("end"), 10, 64)
		if c.EndMs <= c.StartMs || c.StartMs < 0 {
			fail("Select some text first.")
			return
		}
		anchor = c.StartMs
	case "merge":
		c.From, err = strconv.Atoi(r.FormValue("from"))
		if err != nil || c.From < labelCrosstalk || c.From == c.Label {
			fail("Choose a different speaker.")
			return
		}
		if c.From < 0 {
			// "Unknown" and "[crosstalk]" are no voice of their own (no
			// cluster to rename): give each of their lines to the chosen
			// speaker with a passage correction. No voice samples - these
			// lines can be music or several people at once.
			n, err := s.relabelLines(v, corr, c.From, c.Label, c.UserID)
			if err != nil {
				fail(err.Error())
				return
			}
			if n == 0 {
				fail("No lines of " + labelName(c.From) + " left in this version.")
				return
			}
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
	default:
		fail("Unknown correction.")
		return
	}
	cid, err := s.st.AddCorrectionID(v.ID, c)
	if err != nil {
		fail(err.Error())
		return
	}
	// naming a voice or a passage = confirmed voice sample for that person
	if personID > 0 {
		var serr error
		if c.Kind == "range" {
			serr = addSampleFromRange(r.Context(), s.st, v, personID, cid, c.StartMs, c.EndMs)
		} else if c.From < personLabelBase {
			serr = addSampleFromCluster(s.st, v, personID, cid, c.From)
		}
		if serr != nil {
			logf("warning: could not save voice sample: %v", serr)
		}
	}
	dest := to
	if anchor > 0 {
		dest += fmt.Sprintf("#at%d", anchor)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// relabelLines gives every line that currently shows label from (Unknown or
// crosstalk) to label to, one passage correction per line; returns how many.
func (s *Server) relabelLines(v Version, corr []Correction, from, to int, userID int64) (int, error) {
	segs, toks, turns, err := s.st.LoadResults(v.ID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range relabelRanges(buildUtterances(segs, toks, turns, corr), from, to) {
		c.UserID = userID
		if _, err := s.st.AddCorrectionID(v.ID, c); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// relabelRanges: one passage correction per line labelled from, giving it to.
func relabelRanges(us []Utterance, from, to int) []Correction {
	var out []Correction
	for _, u := range us {
		if u.Label == from && u.EndMs > u.StartMs {
			out = append(out, Correction{Kind: "range", StartMs: u.StartMs, EndMs: u.EndMs, Label: to})
		}
	}
	return out
}

func (s *Server) handleVoiceMerge(w http.ResponseWriter, r *http.Request) {
	v, ok := s.correctionVersion(w, r)
	if !ok {
		return
	}
	to := fmt.Sprintf("/episodes/%d?v=%d", v.EpisodeID, v.ID)
	sim := voiceMergeSimilarity(s.st)
	n, err := applyVoiceMerge(s.st, v.ID, sim)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	if sim <= 0 {
		back(w, r, to, "Voice merging is switched off (Setup page) - automatic merges removed.", "")
		return
	}
	logf("Episode %d v%d: merged similar voices (>= %.2f): %d merges", v.EpisodeID, v.ID, sim, n)
	back(w, r, to, fmt.Sprintf("Merged similar voices at %.2f: %d merges. Manual corrections were kept.", sim, n), "")
}

func (s *Server) handleCarriedDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.correctionVersion(w, r)
	if !ok {
		return
	}
	to := fmt.Sprintf("/episodes/%d?v=%d", v.EpisodeID, v.ID)
	n, err := s.st.DeleteCarriedCorrections(v.ID)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, fmt.Sprintf("Removed %d carried-over passages. Speakers now come from this version's own detection and recognition.", n), "")
}

func (s *Server) handleCorrectionDelete(w http.ResponseWriter, r *http.Request) {
	v, ok := s.correctionVersion(w, r)
	if !ok {
		return
	}
	cid, _ := strconv.ParseInt(r.PathValue("cid"), 10, 64)
	s.st.DeleteSamplesOfCorrection(cid)
	if err := s.st.DeleteCorrection(v.ID, cid); err != nil {
		back(w, r, fmt.Sprintf("/episodes/%d?v=%d", v.EpisodeID, v.ID), "", err.Error())
		return
	}
	back(w, r, fmt.Sprintf("/episodes/%d?v=%d", v.EpisodeID, v.ID), "Correction removed.", "")
}

// ------------------------------------------------------------ identify + people

func (s *Server) handleIdentify(w http.ResponseWriter, r *http.Request) {
	v, ok := s.correctionVersion(w, r)
	if !ok {
		return
	}
	to := fmt.Sprintf("/episodes/%d?v=%d", v.EpisodeID, v.ID)
	if s.worker.Busy() {
		back(w, r, to, "", "Something else is running. Stop it first, or wait until it's finished.")
		return
	}
	if err := s.worker.StartIdentify(v); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Identifying speakers with the known voices. The page updates when it's done.", "")
}

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	ps, _ := s.st.People()
	s.render(w, r, "people", "People", "people", map[string]any{"People": ps})
}

func (s *Server) handlePersonAdd(w http.ResponseWriter, r *http.Request) {
	id, err := s.st.AddPerson(r.FormValue("name"))
	if err != nil {
		back(w, r, "/people", "", err.Error())
		return
	}
	refreshPeopleCache(s.st)
	back(w, r, fmt.Sprintf("/people/%d", id), "Person added.", "")
}

func (s *Server) handlePerson(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := s.st.Person(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	samples, _ := s.st.VoiceSamples(id)
	passages, episodes := s.st.PersonUsage(id)
	s.render(w, r, "person", p.Name, "people", map[string]any{"Person": p, "Samples": samples,
		"Passages": passages, "Episodes": episodes})
}

func (s *Server) handlePersonRename(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/people/%d", id)
	if err := s.st.RenamePerson(id, r.FormValue("name")); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	refreshPeopleCache(s.st)
	back(w, r, to, "Name saved.", "")
}

func (s *Server) handlePersonDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.FormValue("confirm") != "1" {
		back(w, r, fmt.Sprintf("/people/%d#remove", id), "", "Tick the box to confirm that the person should be removed.")
		return
	}
	if err := s.st.DeletePerson(id); err != nil {
		back(w, r, fmt.Sprintf("/people/%d", id), "", err.Error())
		return
	}
	refreshPeopleCache(s.st)
	back(w, r, "/people", "Person removed, including their voice samples. Transcripts show \"Removed person\" where they were named.", "")
}

func (s *Server) handleSampleDelete(w http.ResponseWriter, r *http.Request) {
	sid, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	pid, _ := strconv.ParseInt(r.FormValue("person"), 10, 64)
	if err := s.st.DeleteVoiceSample(sid); err != nil {
		back(w, r, fmt.Sprintf("/people/%d", pid), "", err.Error())
		return
	}
	back(w, r, fmt.Sprintf("/people/%d", pid), "Voice sample removed. It no longer influences recognition.", "")
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/feeds/%d", id)
	checked, broken, err := findBrokenTranscripts(s.st, id)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	if broken == 0 {
		back(w, r, to, fmt.Sprintf("Checked %d transcripts - all look fine.", checked), "")
		return
	}
	back(w, r, to, fmt.Sprintf("Checked %d transcripts: %d were broken and are queued again (Waiting). Start transcribing to redo them.", checked, broken), "")
}

// handleRole changes one person's role (the radio buttons save right away).
func (s *Server) handleRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	pid, err := strconv.ParseInt(r.PathValue("pid"), 10, 64)
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	role := r.FormValue("role")
	if role != roleHost && role != rolePool {
		role = ""
	}
	err = s.st.SetRole(id, pid, role)
	if err == nil {
		refreshPeopleCache(s.st)
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"role": role})
		return
	}
	to := fmt.Sprintf("/feeds/%d#people", id)
	if err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Saved.", "")
}

func (s *Server) handleRoster(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	to := fmt.Sprintf("/feeds/%d", id)
	if err := r.ParseForm(); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	roles := map[int64]string{}
	for k, vs := range r.PostForm {
		if !strings.HasPrefix(k, "role_") || len(vs) == 0 {
			continue
		}
		pid, err := strconv.ParseInt(strings.TrimPrefix(k, "role_"), 10, 64)
		if err == nil {
			roles[pid] = vs[0]
		}
	}
	for _, n := range strings.Split(r.FormValue("new_hosts"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			if pid, err := s.st.AddPerson(n); err == nil {
				roles[pid] = roleHost
			}
		}
	}
	for _, n := range strings.Split(r.FormValue("new_pool"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			if pid, err := s.st.AddPerson(n); err == nil && roles[pid] == "" {
				roles[pid] = rolePool
			}
		}
	}
	if err := s.st.SetRoster(id, roles); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	refreshPeopleCache(s.st)
	back(w, r, to, "People of this podcast saved.", "")
}

// warnOtherInstall points out when an installed copy (install.sh) exists but
// a different copy - e.g. the unpacked download folder - is being run. Each
// copy has its own data folder (transcripts, whisper build).
func warnOtherInstall() {
	if runtime.GOOS != "linux" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	inst := filepath.Join(home, ".local", "share", "qs-podscript")
	if !fileExists(filepath.Join(inst, "qs-podscript")) {
		return
	}
	a, _ := filepath.EvalSymlinks(inst)
	b, _ := filepath.EvalSymlinks(P.App)
	if a != "" && a != b {
		logf("NOTE: QS-PodScript is installed in %s, but you are running the copy in %s.", inst, P.App)
		logf("      Each copy has its own data (transcripts, GPU build). Use the installed one: 'qs-podscript' or the app menu.")
	}
}
