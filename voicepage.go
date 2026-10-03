package main

// "All snippets of one voice": click a name in the lanes of an episode and
// check, line by line, who really speaks - the same rows and name buttons as
// in "Look Who's Talking Now". Confirmed lines count as checked; "All of
// them are …" renames the whole voice at once (a merge correction).

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const voicePageRows = 40

func (s *Server) handleVoicePage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	label, err := strconv.Atoi(r.PathValue("label"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ep, err := s.st.Episode(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	feed, _ := s.st.Feed(ep.FeedID)
	vid, err := s.episodeVersion(r, ep)
	if err != nil || vid == 0 {
		http.NotFound(w, r)
		return
	}
	ver, err := s.st.Version(vid)
	if err != nil || ver.Status != "done" {
		http.NotFound(w, r)
		return
	}
	segs, toks, turns, err := s.st.LoadResults(vid)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	corr, _ := s.st.Corrections(vid)
	us := applySpelling(buildUtterances(segs, toks, turns, corr), spellingFor(s.st, ep.FeedID))

	// this voice's lines, cut like in Look Who's Talking Now; ads and movie
	// clips stay in (actors in clips are voices too), with their mark
	var rows, all []quizRow // this voice's lines / every line of the episode (for context)
	marks := map[int64]string{}
	at := map[int64]int{} // start -> index in all
	var totalMs int64
	for _, u := range us {
		if len(u.Words) == 0 {
			continue
		}
		m := u.Mark
		u.Mark = ""
		for _, row := range quizRows([]Utterance{u}) {
			at[row.StartMs] = len(all)
			all = append(all, row)
			marks[row.StartMs] = m
			if u.Label == label {
				rows = append(rows, row)
				totalMs += row.EndMs - row.StartMs
			}
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pages := (len(rows) + voicePageRows - 1) / voicePageRows
	if page > pages && pages > 0 {
		page = pages
	}
	from := (page - 1) * voicePageRows
	to := min(from+voicePageRows, len(rows))
	shown := rows
	if len(rows) > 0 {
		shown = rows[from:to]
	}
	views, others := s.quizRowViews(ep.FeedID, ep.ID, shown, 0)
	ivs := s.st.checkedIntervals(vid, corr)
	type ctxLine struct {
		Name, Class, Text, Clock string
	}
	type voiceRow struct {
		quizRowView
		Mark          string
		Checked       bool
		Before, After []ctxLine
		CtxFrom       int64 // "play with context": from the first line before ...
		CtxTo         int64 // ... to the end of the last line after
	}
	ctx := func(r quizRow) ctxLine {
		var words []string
		for _, w := range r.Words {
			words = append(words, w.Text)
		}
		return ctxLine{labelName(r.Label), spClass(r.Label), strings.Join(words, " "), clockShort(r.StartMs)}
	}
	var out []voiceRow
	checked := 0
	for _, row := range rows {
		if checkState(ivs, row.StartMs, row.EndMs) == "checked" {
			checked++
		}
	}
	const around = 2 // lines of context before and after
	for _, v := range views {
		vr := voiceRow{quizRowView: v, Mark: marks[v.StartMs], Checked: checkState(ivs, v.StartMs, v.EndMs) == "checked",
			CtxFrom: v.StartMs, CtxTo: v.EndMs}
		if i, ok := at[v.StartMs]; ok {
			for k := max(0, i-around); k < i; k++ {
				vr.Before = append(vr.Before, ctx(all[k]))
				vr.CtxFrom = min(vr.CtxFrom, all[k].StartMs)
			}
			for k := i + 1; k < len(all) && k <= i+around; k++ {
				vr.After = append(vr.After, ctx(all[k]))
				vr.CtxTo = max(vr.CtxTo, all[k].EndMs)
			}
		}
		out = append(out, vr)
	}
	// "All of them are …": everybody who can be chosen, plus the other voices
	var allPeople []quizChoice
	chipPs, otherPs := s.st.chipPeople(ep.FeedID, ep.ID)
	for _, p := range append(chipPs, otherPs...) {
		if personLabel(p.ID) != label {
			allPeople = append(allPeople, quizChoice{Value: fmt.Sprintf("p%d", p.ID), Name: p.Name})
		}
	}
	var voices []quizChoice
	seen := map[int]bool{}
	for _, u := range us {
		if _, isPerson := labelPerson(u.Label); isPerson || u.Label < 0 || u.Label == label || seen[u.Label] {
			continue
		}
		seen[u.Label] = true
		voices = append(voices, quizChoice{Value: strconv.Itoa(u.Label), Name: labelName(u.Label)})
	}
	here := fmt.Sprintf("/episodes/%d/voice/%d?v=%d", ep.ID, label, vid)
	data := map[string]any{
		"Episode": ep, "Feed": feed, "Version": ver, "Label": label, "Name": labelName(label),
		"Class": spClass(label), "Rows": out, "Others": others, "Total": len(rows), "Checked": checked,
		"Seconds": float64(totalMs) / 1000, "AudioMs": int64(ver.AudioSeconds*1000) + 2000, "Page": page, "Pages": pages, "From": from + 1, "To": to,
		"Audio": "/audio/" + ver.AudioFile, "AllPeople": allPeople, "Voices": voices, "Here": here,
		"IsPerson": label >= personLabelBase,
	}
	if page > 1 {
		data["Prev"] = fmt.Sprintf("%s&page=%d", here, page-1)
	}
	if page < pages {
		data["Next"] = fmt.Sprintf("%s&page=%d", here, page+1)
	}
	if page < pages {
		data["NextAfterSave"] = fmt.Sprintf("%s&page=%d", here, page+1)
	} else {
		data["NextAfterSave"] = fmt.Sprintf("%s&page=%d", here, page)
	}
	s.render(w, r, "voice", labelName(label)+" – "+ep.Title, "feeds", data)
}
