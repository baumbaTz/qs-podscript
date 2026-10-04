package main

import (
	"context"
	"fmt"
	"sync"
)

// Worker runs at most one background job at a time: processing the queue or
// running setup. The web UI starts/stops jobs through it.
type Worker struct {
	st *Store

	mu         sync.Mutex
	running    bool
	softCancel context.CancelFunc
	hardCancel context.CancelFunc
	asksRun    bool // the running job only does the "asked for" list
}

// StartAsks does the "asked for" list (asks.go) and nothing else.
func (w *Worker) StartAsks() error {
	return w.startJob("queue", true, func(hard, soft context.Context) error {
		return runQueue(hard, soft, w.st, queueOpts{AsksOnly: true})
	})
}

// afterJob: a job ended by itself (not stopped) - now do what was asked for
// meanwhile. Not after a run of the list itself (it ended because the rest
// can't be done right now).
func (w *Worker) afterJob(stopped, wasAsks bool) {
	if stopped || wasAsks || len(w.st.asks()) == 0 || !getSetupState(w.st).Ready() {
		return
	}
	if err := w.StartAsks(); err == nil {
		logf("Doing what was asked for while this computer was busy.")
	}
}

func newWorker(st *Store) *Worker { return &Worker{st: st} }

func (w *Worker) Busy() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

func (w *Worker) start(job string, fn func(hard, soft context.Context) error) error {
	return w.startJob(job, false, fn)
}

func (w *Worker) startJob(job string, asks bool, fn func(hard, soft context.Context) error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return fmt.Errorf("another job is already running")
	}
	hard, hc := context.WithCancel(context.Background())
	soft, sc := context.WithCancel(context.Background())
	w.running, w.hardCancel, w.softCancel, w.asksRun = true, hc, sc, asks
	updateStatus(func(s *Status) {
		s.Job, s.Stage, s.Pct, s.Stopping, s.LastError = job, "Starting", -1, false, ""
	})
	go func() {
		err := fn(hard, soft)
		if err != nil && hard.Err() == nil {
			logf("ERROR: %v", err)
			updateStatus(func(s *Status) { s.LastError = err.Error() })
		}
		stopped := hard.Err() != nil || soft.Err() != nil
		hc()
		sc()
		w.mu.Lock()
		w.running = false
		wasAsks := w.asksRun
		w.mu.Unlock()
		updateStatus(func(s *Status) {
			s.Job, s.Stage, s.Pct, s.Stopping, s.EpisodeID, s.EpisodeTitle = "", "", 0, false, 0, ""
		})
		if job != "setup" {
			w.afterJob(stopped, wasAsks)
		}
	}()
	return nil
}

func (w *Worker) StartQueue(o queueOpts) error {
	return w.start("queue", func(hard, soft context.Context) error {
		return runQueue(hard, soft, w.st, o)
	})
}

// StartHelperJob runs a job already claimed from the server; with more, it
// then keeps taking server work until there is none.
func (w *Worker) StartHelperJob(c remoteConf, job *Job, more bool) error {
	return w.start("queue", func(hard, soft context.Context) error {
		if err := runRemoteJob(hard, w.st, c, job); err != nil {
			if hard.Err() != nil {
				return nil
			}
			logf("   server job: %v", err)
			updateStatus(func(s *Status) { s.LastError = "Server: " + err.Error() })
		}
		if soft.Err() != nil || !more {
			return nil
		}
		return runQueue(hard, soft, w.st, queueOpts{RemoteOnly: true})
	})
}

func (w *Worker) StartSetup(model, gpuMode string, force bool) error {
	return w.start("setup", func(hard, soft context.Context) error {
		return runSetup(hard, w.st, model, gpuMode, force)
	})
}

// StartGPUSpeakers installs (or removes) graphics card support for speaker
// detection; shown like the setup job.
func (w *Worker) StartGPUSpeakers(install bool) error {
	return w.start("setup", func(hard, soft context.Context) error {
		if !install {
			return removeGPUSpeakers()
		}
		_, err := installGPUSpeakers(hard, false)
		return err
	})
}

func (w *Worker) StartRediarize(src Version) error {
	return w.start("queue", func(hard, soft context.Context) error {
		return rediarizeVersion(hard, w.st, src)
	})
}

func (w *Worker) StartIdentify(v Version) error {
	return w.start("queue", func(hard, soft context.Context) error {
		ep, _ := w.st.Episode(v.EpisodeID)
		updateStatus(func(s *Status) { s.EpisodeID, s.EpisodeTitle = ep.ID, ep.Title })
		defer updateStatus(func(s *Status) { s.EpisodeID, s.EpisodeTitle = 0, "" })
		logf("Episode %d v%d: identifying speakers...", v.EpisodeID, v.ID)
		res, err := identifyVersion(hard, w.st, v, nil)
		if err != nil {
			return err
		}
		logf("Episode %d v%d: %s", v.EpisodeID, v.ID, res)
		updateStatus(func(s *Status) { s.DoneCount++ })
		return nil
	})
}

// Stop: first call finishes the current episode then stops (graceful);
// with now=true the job is aborted immediately.
func (w *Worker) Stop(now bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return
	}
	w.softCancel()
	updateStatus(func(s *Status) { s.Stopping = true })
	if now {
		w.hardCancel()
		logf(">> Aborting...")
	} else {
		logf(">> Stopping after the current episode.")
	}
}
