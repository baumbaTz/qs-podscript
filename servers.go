package main

// Saved servers and the active place.
//
// A local QS-PodScript can be connected to several servers. Exactly one
// place is "active": this computer (0) or one of the servers. The active
// place is where you work: with a server active, the Podcasts, Search and
// People pages are the server's (through the pass-through port); Setup,
// Queue and Activity stay this computer's. Switching happens in Setup or with
// the place menu in the header.
//
// Each saved server has its own "Transcribe for this server" tick: "Start
// transcribing" does the own queue first and then takes turns between the
// ticked servers - no matter which place is active.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const (
	setServers     = "servers"      // JSON []remoteConf
	keyActivePlace = "active_place" // "0" = this computer, else a server id
)

var serversMu sync.Mutex

func (c remoteConf) Connected() bool { return c.URL != "" && c.Token != "" }

// Host: the server's address without https:// (for short labels).
func (c remoteConf) Host() string {
	if u, err := url.Parse(c.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return c.URL
}

// Label: the name the user gave it, else the address.
func (c remoteConf) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Host()
}

// IsAdmin: may this computer's user change the server's setup? Unknown (not
// asked yet): assume yes - the server checks anyway.
func (c remoteConf) IsAdmin() bool { return c.Admin != "0" }

// savedServers returns the list, moving a pre-0.26 single connection into it.
func savedServers(st *Store) []remoteConf {
	serversMu.Lock()
	defer serversMu.Unlock()
	return loadServersLocked(st)
}

func loadServersLocked(st *Store) []remoteConf {
	var list []remoteConf
	if raw := st.Setting(setServers, ""); raw != "" {
		json.Unmarshal([]byte(raw), &list)
		return list
	}
	// before 0.26.0: one server in separate settings
	if u, t := st.Setting("remote_url", ""), st.Setting("remote_token", ""); u != "" && t != "" {
		list = []remoteConf{{ID: 1, URL: u, Token: t, User: st.Setting("remote_user", ""),
			Admin: st.Setting("remote_admin", ""), Work: st.Setting("remote_work", "0") == "1"}}
		saveServersLocked(st, list)
		for _, k := range []string{"remote_url", "remote_token", "remote_user", "remote_admin", "remote_work"} {
			st.SetSetting(k, "")
		}
	}
	return list
}

func saveServersLocked(st *Store, list []remoteConf) {
	b, _ := json.Marshal(list)
	st.SetSetting(setServers, string(b))
}

// updateServer changes one saved server (false: not found).
func updateServer(st *Store, id int64, fn func(*remoteConf)) bool {
	serversMu.Lock()
	defer serversMu.Unlock()
	list := loadServersLocked(st)
	for i := range list {
		if list[i].ID == id {
			fn(&list[i])
			saveServersLocked(st, list)
			return true
		}
	}
	return false
}

// addServer stores a new connection, or renews the one with the same
// address; returns its id.
func addServer(st *Store, c remoteConf) int64 {
	serversMu.Lock()
	defer serversMu.Unlock()
	list := loadServersLocked(st)
	var maxID int64
	for i := range list {
		if list[i].URL == c.URL {
			c.ID, c.Name, c.Work = list[i].ID, list[i].Name, list[i].Work
			list[i] = c
			saveServersLocked(st, list)
			return c.ID
		}
		if list[i].ID > maxID {
			maxID = list[i].ID
		}
	}
	c.ID = maxID + 1
	saveServersLocked(st, append(list, c))
	return c.ID
}

func removeServer(st *Store, id int64) {
	serversMu.Lock()
	list := loadServersLocked(st)
	out := list[:0]
	for _, c := range list {
		if c.ID != id {
			out = append(out, c)
		}
	}
	saveServersLocked(st, out)
	serversMu.Unlock()
	if activePlace(st) == id {
		setActivePlace(st, 0)
	}
	st.dropServerAsks(id)
}

func serverByID(st *Store, id int64) (remoteConf, bool) {
	for _, c := range savedServers(st) {
		if c.ID == id && c.Connected() {
			return c, true
		}
	}
	return remoteConf{}, false
}

// activePlace: 0 = this computer, else the id of the active server.
func activePlace(st *Store) int64 {
	id, _ := strconv.ParseInt(st.Setting(keyActivePlace, "0"), 10, 64)
	return id
}

func setActivePlace(st *Store, id int64) {
	st.SetSetting(keyActivePlace, strconv.FormatInt(id, 10))
}

// activeServer: the server you work on right now (ok = false: this computer).
func activeServer(st *Store) (remoteConf, bool) {
	id := activePlace(st)
	if id == 0 {
		return remoteConf{}, false
	}
	return serverByID(st, id)
}

// ---------------------------------------------------------------- the place menu

// place: one entry of the place menu (header) - also sent to the server, so
// its pages can show the same menu.
type place struct {
	ID     int64  `json:"id"` // 0 = this computer
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func placeList(st *Store) []place {
	act := activePlace(st)
	out := []place{{ID: 0, Name: "This computer", Active: act == 0}}
	for _, c := range savedServers(st) {
		if c.Connected() {
			out = append(out, place{ID: c.ID, Name: c.Label(), Active: c.ID == act})
		}
	}
	if len(out) > 1 && !out[0].Active {
		// active server gone (removed): this computer
		found := false
		for _, p := range out[1:] {
			found = found || p.Active
		}
		out[0].Active = !found
	}
	return out
}

// placesHeader / parsePlacesHeader: the place menu for the server's pages.
const placesHeader = "X-QSPodScript-Places"

func encodePlaces(ps []place) string {
	b, _ := json.Marshal(ps)
	return url.QueryEscape(string(b))
}

func decodePlaces(h string) []place {
	if h == "" || len(h) > 8000 {
		return nil
	}
	raw, err := url.QueryUnescape(h)
	if err != nil {
		return nil
	}
	var ps []place
	if json.Unmarshal([]byte(raw), &ps) != nil {
		return nil
	}
	for i := range ps {
		if len(ps[i].Name) > 80 {
			ps[i].Name = ps[i].Name[:80]
		}
	}
	return ps
}

// handleSwitch: POST /switch (local, also /_local/switch from server pages).
func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("place"), 10, 64)
	if id != 0 {
		if _, ok := serverByID(s.st, id); !ok {
			back(w, r, "/setup#places", "", "That server is not saved any more.")
			return
		}
	}
	setActivePlace(s.st, id)
	searchKick() // nothing to do with it, but cheap: the local index stays fresh
	if id == 0 {
		logf("Now working on this computer")
		http.Redirect(w, r, strings.TrimRight(s.localURL, "/")+"/", http.StatusSeeOther)
		return
	}
	c, _ := serverByID(s.st, id)
	logf("Now working on the server %s", c.Label())
	if addr := passThroughAddr(); addr != "" {
		http.Redirect(w, r, addr+"/", http.StatusSeeOther)
		return
	}
	back(w, r, "/setup#places", "", "The server pages could not be opened (see Activity).")
}

// ---------------------------------------------------------------- Setup: managing the servers

func sidOf(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	return id
}

func (s *Server) handleRemoteConnect(w http.ResponseWriter, r *http.Request) {
	to := "/setup#places"
	id, err := connectRemote(r.Context(), s.st, r.FormValue("url"), r.FormValue("name"), r.FormValue("password"))
	if err != nil {
		back(w, r, to, "", "Could not connect: "+err.Error())
		return
	}
	if label := strings.TrimSpace(r.FormValue("label")); label != "" {
		updateServer(s.st, id, func(c *remoteConf) { c.Name = limitLen(label, 60) })
	}
	c, _ := serverByID(s.st, id)
	logf("Connected to %s as %s", c.URL, c.User)
	if _, err := startPassThrough(s.st, s.localURL, s.proxyPort); err != nil {
		back(w, r, to, "", err.Error())
		return
	}
	back(w, r, to, "Connected to "+c.Label()+" as "+c.User+". Switch to it under “Where you work” (or with the menu next to the name at the top) to work on its podcasts.", "")
}

func (s *Server) handleRemoteDisconnect(w http.ResponseWriter, r *http.Request) {
	c, ok := serverByID(s.st, sidOf(r))
	if !ok {
		back(w, r, "/setup#places", "", "That server is not saved.")
		return
	}
	removeServer(s.st, c.ID)
	logf("Removed the server %s", c.Label())
	back(w, r, "/setup#places", "Removed "+c.Label()+". Its key on the server stays until an admin removes it there (or you, under your account on the server).", "")
}

func (s *Server) handleRemoteRename(w http.ResponseWriter, r *http.Request) {
	name := limitLen(strings.TrimSpace(r.FormValue("name")), 60)
	updateServer(s.st, sidOf(r), func(c *remoteConf) { c.Name = name })
	back(w, r, "/setup#places", "Saved.", "")
}

func (s *Server) handleRemoteWork(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("work") == "1"
	var label string
	updateServer(s.st, sidOf(r), func(c *remoteConf) { c.Work, label = on, c.Label() })
	msg := "This computer no longer transcribes for " + label + "."
	if on {
		msg = "“Start transcribing” continues with episodes for " + label + " after your own."
	}
	back(w, r, "/setup#places", msg, "")
}

func limitLen(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// placeRedirect: with a server active, this computer's podcast pages lead
// to the server's - you can't land on your own podcasts by accident (old
// links, bookmarks). Setup, Queue, Activity and Help stay here.
func (s *Server) placeRedirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && contentPage(r.URL.Path) {
			if _, ok := activeServer(s.st); ok {
				if addr := passThroughAddr(); addr != "" {
					to := addr + r.URL.Path
					if r.URL.RawQuery != "" {
						to += "?" + r.URL.RawQuery
					}
					http.Redirect(w, r, to, http.StatusSeeOther)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func contentPage(p string) bool {
	return p == "/" || p == "/search" || p == "/people" ||
		strings.HasPrefix(p, "/feeds/") || strings.HasPrefix(p, "/episodes/") || strings.HasPrefix(p, "/people/")
}

// activeServerForSetup: the active server (nil: this computer), its admin
// flag freshly asked - for the "Server" tab of Setup and Activity.
func activeServerForSetup(r *http.Request, st *Store) *remoteConf {
	c, ok := activeServer(st)
	if !ok {
		return nil
	}
	refreshServerAdmin(r.Context(), st, c)
	c, _ = serverByID(st, c.ID)
	return &c
}
