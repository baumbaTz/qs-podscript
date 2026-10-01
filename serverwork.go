package main

// Who does the heavy work on a homeserver: by default the helpers' computers
// (connected QS-PodScripts with "Transcribe for the server"). The server
// itself only transcribes when that is switched on (e.g. it has its own
// graphics card). Buttons like "Transcribe again" or "Redo speaker
// detection" then queue the work for the helpers instead of running it here.

import "net/http"

func (s *Server) helpersOnly() bool {
	return s.serverMode && s.st.Setting("server_transcribes", "0") != "1"
}

type helperWork struct {
	Waiting int // episodes waiting for a helper
	Working int // reserved by a helper right now
}

func (s *Store) helperWork() helperWork {
	var h helperWork
	s.db.QueryRow(`SELECT COALESCE(SUM(CASE WHEN status IN ('new','queued') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='leased' THEN 1 ELSE 0 END),0) FROM episodes`).Scan(&h.Waiting, &h.Working)
	return h
}

func (s *Server) handleServerWork(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("self") == "1"
	s.st.SetSetting("server_transcribes", map[bool]string{true: "1", false: "0"}[on])
	if on {
		back(w, r, "/setup#server-work", "This server now transcribes too – “Start transcribing” works here again.", "")
		return
	}
	if s.worker.Busy() {
		back(w, r, "/setup#server-work", "Saved. The job that is running here finishes first; after that only the helpers' computers transcribe.", "")
		return
	}
	back(w, r, "/setup#server-work", "Saved: only the helpers' computers transcribe.", "")
}

// helperQueuedMsg: what happens after "Transcribe again" / "Redo speaker
// detection" on the server.
func helperQueuedMsg(r *http.Request, what string) string {
	switch {
	case r.Header.Get(localIdleHeader) == "1":
		return "Your computer " + what + " now – its progress is shown at the top right."
	case r.Header.Get(localIdleHeader) == "0":
		return "Your computer is busy right now – this episode is on its list and is done right after the current work (or first thing when you press “Start transcribing”)."
	}
	return "Queued for the helpers (first in line): it is done when someone presses “Start transcribing” in their QS-PodScript with “Transcribe for the server” ticked, or clicks this button on the server page in their QS-PodScript."
}
