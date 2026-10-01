package main

import (
	"sync"
	"time"
)

// Status is the live state of the background job, pushed to the browser via SSE.
type Status struct {
	Job             string `json:"job"`   // "" (idle), "queue", "setup"
	Stage           string `json:"stage"` // human-readable current step
	Pct             int    `json:"pct"`   // 0-100, -1 = unknown
	EpisodeID       int64  `json:"episodeId"`
	EpisodeTitle    string `json:"episodeTitle"`
	ServerEpisodeID int64  `json:"serverEpisodeId,omitempty"` // helping: the episode on the server
	ServerID        int64  `json:"serverId,omitempty"`        // ... on which saved server
	Stopping        bool   `json:"stopping"`
	LastError       string `json:"lastError"`
	DoneCount       int    `json:"doneCount"` // increments when an episode finishes
	Updated         int64  `json:"updated"`
}

var (
	statusMu   sync.Mutex
	curStatus  Status
	statusSubs = map[chan Status]struct{}{}

	logRing    []string
	logRingMax = 400
	logCount   int // total lines ever logged (for "what's new since")
)

func getStatus() Status {
	statusMu.Lock()
	defer statusMu.Unlock()
	return curStatus
}

// updateStatus applies f to the status and notifies subscribers.
func updateStatus(f func(s *Status)) {
	statusMu.Lock()
	f(&curStatus)
	curStatus.Updated = time.Now().UnixMilli()
	s := curStatus
	for ch := range statusSubs {
		select {
		case ch <- s:
		default: // slow subscriber: drop the stale value and put the new one in
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- s:
			default:
			}
		}
	}
	statusMu.Unlock()
}

func setStage(stage string, pct int) {
	updateStatus(func(s *Status) { s.Stage, s.Pct = stage, pct })
}

func subscribeStatus() (chan Status, func()) {
	ch := make(chan Status, 1)
	statusMu.Lock()
	statusSubs[ch] = struct{}{}
	ch <- curStatus
	statusMu.Unlock()
	return ch, func() {
		statusMu.Lock()
		delete(statusSubs, ch)
		statusMu.Unlock()
	}
}

func appendLogRing(line string) {
	// called with logMu held
	logCount++
	logRing = append(logRing, line)
	if len(logRing) > logRingMax {
		logRing = logRing[len(logRing)-logRingMax:]
	}
}

func recentLog() []string {
	logMu.Lock()
	defer logMu.Unlock()
	out := make([]string, len(logRing))
	copy(out, logRing)
	return out
}

func logSeq() int {
	logMu.Lock()
	defer logMu.Unlock()
	return logCount
}

// logSince returns lines logged after sequence number seen.
func logSince(seen int) ([]string, int) {
	logMu.Lock()
	defer logMu.Unlock()
	n := logCount - seen
	if n <= 0 {
		return nil, logCount
	}
	if n > len(logRing) {
		n = len(logRing)
	}
	out := make([]string, n)
	copy(out, logRing[len(logRing)-n:])
	return out, logCount
}
