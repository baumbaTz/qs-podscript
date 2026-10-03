package main

// Looks: "quicksack" (the default since 0.32.0; the style of quicksack.li:
// near-black, dark red, condensed headings - dark or bright) and "classic"
// (calm and plain, bright or dark). Installs that saved a look keep it. On your own
// computer the choice is a setting; the pages of the active server get it
// passed along (header), so everything you see has the same look. Visitors
// of a server see the look its admin chose.

import "net/http"

const (
	lookHeader  = "X-QSPodScript-Look"
	lookClassic = "classic"
	lookQS      = "quicksack"
	lookDefault = lookQS
)

func validLook(l string) bool { return l == lookClassic || l == lookQS }

func (s *Server) lookFor(r *http.Request, viaLocal bool) string {
	if viaLocal {
		if l := r.Header.Get(lookHeader); validLook(l) {
			return l
		}
	}
	if l := s.st.Setting("look", lookDefault); validLook(l) {
		return l
	}
	return lookDefault
}

func (s *Server) handleLook(w http.ResponseWriter, r *http.Request) {
	l := r.FormValue("look")
	if !validLook(l) {
		back(w, r, "/setup#look", "", "Unknown look.")
		return
	}
	s.st.SetSetting("look", l)
	msg := "Look saved."
	if s.serverMode {
		msg = "Look saved – visitors of this server see it (helpers see the look chosen on their own computer)."
	}
	back(w, r, "/setup#look", msg, "")
}
