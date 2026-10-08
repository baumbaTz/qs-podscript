// QS-PodScript UI – no libraries. Everything also works without JS except live
// updates and click-to-play.
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);
  // settings remembered in this browser (keys from before the rename are read too)
  const lsGet = (k) => {
    try { const v = localStorage.getItem("qs-podscript-" + k); return v !== null ? v : localStorage.getItem("podscribe-" + k); }
    catch (e) { return null; }
  };
  const lsSet = (k, v) => { try { localStorage.setItem("qs-podscript-" + k, v); } catch (e) { /* storage blocked */ } };

  // ---------------------------------------------------------- live job status
  const job = $("job");
  const logEl = document.querySelector("pre[data-live]");
  const autoreload = document.querySelector("[data-autoreload]");
  // server pages opened through the local app: the local count is unknown until the first update
  let lastDone = job ? (job.dataset.local === "1" ? null : Number(job.dataset.done)) : 0;

  function renderStatus(s) {
    if (!job) return;
    job.dataset.job = s.job;
    $("job-state").textContent = s.job === "" ? "Idle" : s.job === "setup" ? "Installing" : "Transcribing";
    $("job-stage").textContent = s.stopping && s.job ? "Stopping… " + s.stage : s.stage;
    const ep = $("job-ep");
    ep.textContent = s.episodeTitle || "";
    // on server pages the local computer's own episodes aren't on this server
    const localJob = job.dataset.local === "1";
    if (s.episodeId && !localJob) ep.href = "/episodes/" + s.episodeId;
    else if (s.serverEpisodeId && localJob && String(s.serverId) === (job.dataset.server || "")) ep.href = "/episodes/" + s.serverEpisodeId;
    else ep.removeAttribute("href");
    $("job-meter").hidden = s.job === "";
    const bar = $("job-bar");
    bar.classList.toggle("busy", s.pct < 0);
    bar.style.width = (s.pct < 0 ? 100 : s.pct) + "%";
    const err = $("job-err");
    err.textContent = s.lastError || "";
    err.hidden = !s.lastError;
    $("job-start").hidden = s.job !== "";
    $("job-stop").hidden = s.job === "";
    $("job-stop-soft").hidden = s.stopping || s.job === "setup";

    // a finished episode changes the list – refresh it, unless the user is typing
    if (lastDone === null) lastDone = s.doneCount;
    if (s.doneCount !== lastDone) {
      lastDone = s.doneCount;
      const busyTyping = document.activeElement && /INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName);
      if (autoreload && !busyTyping) location.reload();
    }
  }

  if (job && window.EventSource) {
    const es = new EventSource((job.dataset.events || "/events") + (logEl && !job.dataset.events ? "?log=1" : ""));
    es.addEventListener("status", (e) => renderStatus(JSON.parse(e.data)));
    if (logEl) {
      logEl.scrollTop = logEl.scrollHeight;
      es.addEventListener("log", (e) => {
        const atBottom = logEl.scrollTop + logEl.clientHeight >= logEl.scrollHeight - 20;
        logEl.append(e.data + "\n");
        if (atBottom) logEl.scrollTop = logEl.scrollHeight;
      });
    }
    // EventSource reconnects by itself when QS-PodScript restarts
  }

  // ---------------------------------------------------------- bright / dark mode
  // dark unless the browser prefers light; the button's choice is remembered
  // in this browser (the <head> sets it before the page is drawn)
  const themenav = $("themenav");
  if (themenav) {
    const root = document.documentElement;
    const btn = $("themebtn");
    const btn2 = $("themebtn2"); // in the header on phones
    const prefersLight = window.matchMedia("(prefers-color-scheme: light)");
    const SUN = '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">' +
      '<circle cx="12" cy="12" r="4.2" fill="currentColor"/><path d="M12 2v2.5M12 19.5V22M2 12h2.5M19.5 12H22M4.9 4.9l1.8 1.8M17.3 17.3l1.8 1.8M4.9 19.1l1.8-1.8M17.3 6.7l1.8-1.8"/></svg>';
    const MOON = '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true" fill="currentColor">' +
      '<path d="M20.5 14.6A8.5 8.5 0 0 1 9.4 3.5a8.5 8.5 0 1 0 11.1 11.1z"/></svg>';
    const current = () => root.dataset.theme || (prefersLight.matches ? "light" : "dark");
    const show = () => {
      const dark = current() === "dark";
      // drawn icons in the text colour (the font's moon glyph came out pale)
      btn.innerHTML = dark ? SUN : MOON;
      // the button previews the mode it switches to
      themenav.classList.toggle("to-light", dark);
      themenav.classList.toggle("to-dark", !dark);
      btn.title = dark ? "Switch to bright mode" : "Switch to dark mode";
      btn.setAttribute("aria-label", btn.title);
      if (btn2) {
        btn2.innerHTML = btn.innerHTML;
        btn2.title = btn.title;
        btn2.setAttribute("aria-label", btn.title);
        btn2.hidden = false;
      }
    };
    const flip = () => {
      const next = current() === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      try { lsSet("theme", next); } catch (e) { /* storage blocked */ }
      show();
    };
    btn.addEventListener("click", flip);
    if (btn2) btn2.addEventListener("click", flip);
    prefersLight.addEventListener("change", show);
    show();
    themenav.hidden = false;
  }

  // ---------------------------------------------------------- on/off switches
  // forms marked data-switch are sent in the background: the switch spins
  // until the server answered, then shows the new state (no page reload).
  // Without JS the form simply submits and the page reloads.
  document.addEventListener("submit", async (e) => {
    const form = e.target.closest("form[data-switch]");
    if (!form || !window.fetch) return;
    e.preventDefault();
    const btn = form.querySelector("button.switch");
    const input = form.querySelector('input[name="all"]');
    if (!btn || btn.classList.contains("busy")) return;
    btn.classList.add("busy");
    btn.setAttribute("aria-busy", "true");
    const started = Date.now();
    try {
      const res = await fetch(form.action, {
        method: "POST", body: new URLSearchParams(new FormData(form)),
        headers: { "Accept": "application/json" },
      });
      const data = await res.json().catch(() => ({}));
      // keep the spinner visible for a moment, so the click is noticed
      await new Promise((r) => setTimeout(r, Math.max(0, 250 - (Date.now() - started))));
      if (!res.ok) throw new Error(data.error || ("HTTP " + res.status));
      const on = !!data.all;
      btn.setAttribute("aria-checked", on ? "true" : "false");
      btn.title = on ? "Used for all podcasts. Click to use it for this podcast only."
                     : "Used for this podcast only. Click to use it for all podcasts.";
      if (input) input.value = on ? "0" : "1";
    } catch (err) {
      btn.title = "Could not save: " + err.message + ". Click to try again.";
      btn.classList.add("failed");
      setTimeout(() => btn.classList.remove("failed"), 1500);
    } finally {
      btn.classList.remove("busy");
      btn.removeAttribute("aria-busy");
    }
  });

  // ---------------------------------------------------------- podcast people
  // choosing Host / Regular / Not on this podcast saves at once and moves the
  // person into that list (alphabetical). Without JS: "Save people" saves all.
  const rosterBox = document.querySelector(".roster-groups");
  if (rosterBox && window.fetch) {
    document.querySelectorAll(".js-only").forEach((el) => { el.hidden = false; });
    const feed = rosterBox.dataset.feed;
    const refresh = () => {
      rosterBox.querySelectorAll("tbody[data-role]").forEach((tb) => {
        const n = tb.querySelectorAll("tr[data-pid]").length;
        tb.querySelector(".empty-row").hidden = n > 0;
        const c = tb.closest(".roster-group").querySelector(".count");
        if (c) c.textContent = n;
      });
    };
    rosterBox.addEventListener("change", async (e) => {
      const input = e.target.closest('input[type="radio"]');
      if (!input) return;
      const row = input.closest("tr[data-pid]");
      const note = row.querySelector(".saving");
      const before = row.closest("tbody").dataset.role;
      note.textContent = "saving…";
      note.className = "saving busy";
      try {
        const res = await fetch("/feeds/" + feed + "/roster/" + row.dataset.pid, {
          method: "POST", body: new URLSearchParams({ role: input.value }),
          headers: { "Accept": "application/json" },
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || ("HTTP " + res.status));
        const target = rosterBox.querySelector('tbody[data-role="' + data.role + '"]');
        // keep the list alphabetical
        const rows = Array.from(target.querySelectorAll("tr[data-pid]"));
        const next = rows.find((r) => r.dataset.name.localeCompare(row.dataset.name, undefined, { sensitivity: "base" }) > 0);
        target.insertBefore(row, next || target.querySelector(".empty-row"));
        refresh();
        note.textContent = "saved";
        note.className = "saving ok";
        row.classList.add("moved");
        setTimeout(() => { row.classList.remove("moved"); if (note.textContent === "saved") note.textContent = ""; }, 1500);
      } catch (err) {
        // put the radio back to what is stored
        const old = row.querySelector('input[value="' + before + '"]');
        if (old) old.checked = true;
        note.textContent = "not saved: " + err.message;
        note.className = "saving failed";
      }
    });
  }

  // ---------------------------------------------------------- compact header
  // the header is sticky (CSS); it only gets smaller once scrolled. The space
  // it gives up is kept as margin below it, so the page doesn't jump and the
  // scroll position can't make it flip back and forth.
  const top = document.querySelector(".top");
  if (top) {
    let compact = false, queued = false, fullH = top.offsetHeight;
    const update = () => {
      queued = false;
      const y = window.scrollY;
      const want = compact ? y >= 8 : y > 80;
      if (!compact && !want) fullH = top.offsetHeight; // job status may change its height
      if (want === compact && !want) return;
      compact = want;
      top.classList.toggle("compact", compact);
      top.style.marginBottom = compact ? Math.max(0, fullH - top.offsetHeight) + "px" : "";
    };
    window.addEventListener("resize", () => { if (!compact) fullH = top.offsetHeight; });
    window.addEventListener("scroll", () => { if (!queued) { queued = true; requestAnimationFrame(update); } }, { passive: true });
    update();
  }

  // ---------------------------------------------------------- page navigation
  const pagenav = $("pagenav");
  if (pagenav) {
    const playerH = () => { const p = document.querySelector(".player"); return p ? p.offsetHeight : 0; };
    const headH = () => { const h = document.querySelector(".top"); return h ? h.offsetHeight : 0; };
    const page = () => Math.max(200, window.innerHeight - playerH() - headH() - 60);
    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const go = (top) => window.scrollTo({ top: top, behavior: reduced ? "auto" : "smooth" });
    pagenav.addEventListener("click", (e) => {
      const b = e.target.closest("button");
      if (!b) return;
      switch (b.dataset.go) {
        case "top": go(0); break;
        case "up": go(window.scrollY - page()); break;
        case "down": go(window.scrollY + page()); break;
        case "bottom": go(document.documentElement.scrollHeight); break;
        case "now": {
          const n = document.querySelector(".u.now");
          if (n) n.scrollIntoView({ block: "center", behavior: reduced ? "auto" : "smooth" });
          break;
        }
      }
    });
    // only when the page is long enough to scroll
    const check = () => { pagenav.hidden = document.documentElement.scrollHeight <= window.innerHeight + 40; };
    check();
    window.addEventListener("resize", check);
    window.addEventListener("load", check);
  }

  // ---------------------------------------------------------- speaker corrections
  const transcript = $("transcript");
  const lanes = $("lanes");
  const menu = $("assign");

  const clock = (ms) => {
    const s = Math.floor(ms / 1000);
    const p = (n) => String(n).padStart(2, "0");
    return p(Math.floor(s / 3600)) + ":" + p(Math.floor(s / 60) % 60) + ":" + p(s % 60);
  };

  let menuStart = null;
  function openMenu(opts, rect) {
    $("assign-what").textContent = opts.what;
    const vl = $("a-voice-link");
    if (vl) { vl.hidden = !opts.voiceHref; if (opts.voiceHref) vl.href = opts.voiceHref; }
    menuStart = opts.kind === "range" ? Number(opts.start) : null;
    const pb = $("a-play");
    if (pb) pb.hidden = menuStart === null || !$("audio");
    $("a-kind").value = opts.kind;
    $("a-start").value = opts.start || "";
    $("a-end").value = opts.end || "";
    $("a-from").value = opts.from != null ? opts.from : "";
    const sel = $("a-label");
    sel.value = "";
    // the voice's own button is hidden ("Unknown" / "Several at once" are -1 / -2 in the lanes, u / x on the buttons)
    const self = opts.from != null ? ({ "-1": "u", "-2": "x" }[String(opts.from)] || String(opts.from)) : null;
    menu.querySelectorAll("[data-label], button[name=label][value=u], button[name=label][value=x]").forEach((o) => {
      o.hidden = self !== null && (o.dataset.label || o.value) === self;
    });
    // lines of "Unknown" / "[crosstalk]" are not saved as voice samples (music, several people)
    const note = menu.querySelector(".assign-note");
    if (note) note.hidden = opts.from != null && Number(opts.from) < 0;
    if ($("a-submit")) $("a-submit").hidden = true; // chips send at once; "Assign" is for the list
    $("a-name").hidden = true;
    $("a-name").required = false;
    // text correction only for passages, not for "merge whole voice"
    const mf = $("a-markform");
    if (mf) {
      mf.hidden = opts.kind !== "range";
      $("a-mstart").value = opts.start || "";
      $("a-mend").value = opts.end || "";
    }
    const tf = $("a-textform");
    if (tf) {
      tf.hidden = opts.kind !== "range";
      $("a-tstart").value = opts.start || "";
      $("a-tend").value = opts.end || "";
      $("a-text").value = opts.text || "";
    }
    menu.hidden = false;
    // open upwards when there is no room below (the audio player sits at the bottom)
    const player = document.querySelector(".player");
    const bottomLimit = window.innerHeight - (player ? player.offsetHeight : 0) - 8;
    const head = document.querySelector(".top");
    const topLimit = head ? head.getBoundingClientRect().bottom + 8 : 8;
    menu.style.maxHeight = "";
    const spaceBelow = bottomLimit - rect.bottom - 8, spaceAbove = rect.top - topLimit - 8;
    let h = menu.offsetHeight, below;
    if (h <= spaceBelow) below = true;
    else if (h <= spaceAbove) below = false;
    else { // fits nowhere: the bigger side, scrollable
      below = spaceBelow >= spaceAbove;
      h = Math.max(160, below ? spaceBelow : spaceAbove);
      menu.style.maxHeight = h + "px";
    }
    const top = below ? window.scrollY + rect.bottom + 8 : window.scrollY + rect.top - h - 8;
    const left = Math.max(8, Math.min(window.scrollX + rect.left, window.scrollX + document.documentElement.clientWidth - menu.offsetWidth - 8));
    menu.style.top = top + "px";
    menu.style.left = left + "px";
    const firstChip = menu.querySelector(".assign-chips button:not([hidden])");
    (firstChip || sel).focus();
  }
  function closeMenu() {
    if (!menu) return;
    menu.hidden = true;
    document.querySelectorAll(".u.picked").forEach((u) => u.classList.remove("picked"));
  }

  if (transcript && menu) {
    $("assign-cancel").addEventListener("click", closeMenu);
    const playBtn = $("a-play");
    if (playBtn) playBtn.addEventListener("click", () => {
      const au = $("audio");
      if (au && menuStart !== null) {
        // start at the full second before the word, so it isn't missed
        au.currentTime = Math.floor(menuStart / 1000);
        au.play();
      }
      closeMenu();
    });

    // click (or tap) a word -> menu for that word: play from here, who said
    // it, correct it. Double-click -> just play from there, no menu. So a
    // click waits a moment to see whether a second one follows. (On a touch
    // screen a long press selects the word, which opens the same menu.)
    let clickTimer = 0;
    const playFromWord = (w) => {
      const au = $("audio");
      if (!au) return;
      // start at the full second before the word, so it isn't missed
      au.currentTime = Math.floor(Number(w.dataset.s) / 1000);
      au.play();
    };
    transcript.addEventListener("click", (e) => {
      const w = e.target.closest("span[data-s]");
      if (!w || e.target.closest(".who, .ts")) return;
      clearTimeout(clickTimer);
      if (e.detail > 1) return; // 2nd click of a double-click: "dblclick" plays
      const sel = window.getSelection();
      if (sel && !sel.isCollapsed) return; // selected words have their own menu
      clickTimer = setTimeout(() => {
        closeMenu();
        const start = Number(w.dataset.s);
        openMenu({
          kind: "range", start: start, end: Math.max(Number(w.dataset.e), start + 1),
          text: w.textContent,
          what: "\u201c" + w.textContent + "\u201d (" + clock(start) + ") is spoken by:",
        }, w.getBoundingClientRect());
      }, 250);
    });
    transcript.addEventListener("dblclick", (e) => {
      const w = e.target.closest("span[data-s]");
      if (!w || e.target.closest(".who, .ts")) return;
      clearTimeout(clickTimer);
      // the browser selects the word on double-click; drop that selection so
      // the selection menu doesn't open
      const sel = window.getSelection();
      if (sel) sel.removeAllRanges();
      closeMenu();
      playFromWord(w);
    });
    $("a-label").addEventListener("change", (e) => {
      if ($("a-submit")) $("a-submit").hidden = false;
      const np = e.target.value === "newperson";
      $("a-name").hidden = !np;
      $("a-name").required = np;
      if (np) $("a-name").focus();
    });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeMenu(); });
    document.addEventListener("mousedown", (e) => {
      if (!menu.hidden && !menu.contains(e.target) && !e.target.closest(".who, .lane-name")) closeMenu();
    });

    // click a speaker name in the text -> this paragraph
    transcript.addEventListener("click", (e) => {
      const who = e.target.closest(".who");
      if (!who) return;
      const u = who.closest(".u");
      closeMenu();
      u.classList.add("picked");
      openMenu({
        kind: "range", start: u.dataset.ms, end: Number(u.dataset.end) + 1,
        text: Array.from(u.querySelectorAll("span[data-s]")).map((w) => w.textContent).join(" "),
        what: "This paragraph (" + clock(Number(u.dataset.ms)) + ") is spoken by:",
      }, who.getBoundingClientRect());
    });

    // select words (also across paragraphs) -> exactly those words
    const onSelect = () => {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed || !sel.rangeCount) return;
      const range = sel.getRangeAt(0);
      if (!transcript.contains(range.commonAncestorContainer)) return;
      const words = Array.from(transcript.querySelectorAll("span[data-s]")).filter((w) => sel.containsNode(w, true));
      if (!words.length) return;
      const start = Number(words[0].dataset.s);
      const end = Number(words[words.length - 1].dataset.e);
      openMenu({
        kind: "range", start: start, end: Math.max(end, start + 1),
        text: words.map((w) => w.textContent).join(" "),
        what: words.length + (words.length === 1 ? " word" : " words") + " (" + clock(start) + ") spoken by:",
      }, range.getBoundingClientRect());
    };
    transcript.addEventListener("mouseup", () => setTimeout(onSelect, 0));
    transcript.addEventListener("touchend", () => setTimeout(onSelect, 300));
  }

  // click a speaker name in the lanes -> name the whole voice at once; the
  // menu links to the line-by-line page (without JS the name is that link)
  if (lanes && menu) {
    lanes.addEventListener("click", (e) => {
      const name = e.target.closest("a.lane-name[data-label]");
      if (!name) return;
      e.preventDefault();
      openMenu({
        kind: "merge", from: name.dataset.label, voiceHref: name.href,
        what: "Everything said by " + name.firstChild.textContent.trim() + " is actually:",
      }, name.getBoundingClientRect());
    });
  }

  // "Hide ads" / "Hide movie clips": the hiding itself is pure CSS; this only
  // remembers the choice across the page reloads after each correction
  ["hide-ads", "hide-clips", "hl-unchecked"].forEach((id) => {
    const cb = $(id);
    if (!cb) return;
    // remembered choice wins; never chosen -> the page's default (highlighting: on)
    try { const v = lsGet("" + id); if (v !== null) cb.checked = v === "1"; } catch (e) { /* storage blocked */ }
    cb.addEventListener("change", () => {
      try { lsSet("" + id, cb.checked ? "1" : "0"); } catch (e) { /* ignore */ }
    });
  });

  // after a correction the page returns with #at<ms>: show that spot again
  const at = location.hash.match(/^#at(\d+)$/);
  if (at && transcript) {
    const ms = Number(at[1]);
    const line = Array.from(transcript.querySelectorAll(".u")).filter((u) => Number(u.dataset.ms) <= ms).pop();
    if (line) line.scrollIntoView({ block: "center" });
  }

  // ---------------------------------------------------------- place menu: close when clicking elsewhere
  const placeMenu = document.querySelector("details.placemenu");
  if (placeMenu) {
    document.addEventListener("click", (e) => { if (placeMenu.open && !placeMenu.contains(e.target)) placeMenu.open = false; });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") placeMenu.open = false; });
  }

  // ---------------------------------------------------------- queue: drag to reorder
  // Hold the handle, move, let go. The new order of the listed episodes is
  // saved at once; the numbers follow while dragging. Without JS the
  // "To the top / To the end" buttons do the same job.
  const qtable = document.querySelector("table.queue[data-sortable]");
  if (qtable && window.fetch) {
    const tbody = qtable.tBodies[0];
    qtable.classList.add("js-sortable");
    const hint = document.querySelector(".drag-hint");
    if (hint) hint.hidden = false;
    const ids = () => Array.from(tbody.rows).map((r) => r.dataset.id).join(",");
    const renumber = () => Array.from(tbody.rows).forEach((r, i) => {
      const n = r.querySelector("td.num");
      if (n) n.textContent = String(i + 1);
    });
    let drag = null;
    tbody.addEventListener("pointerdown", (e) => {
      const h = e.target.closest(".drag-handle");
      if (!h || e.button > 0) return;
      e.preventDefault();
      drag = { row: h.closest("tr"), before: ids() };
      drag.row.classList.add("dragging");
      h.setPointerCapture(e.pointerId);
    });
    tbody.addEventListener("pointermove", (e) => {
      if (!drag) return;
      const y = e.clientY;
      let target = null;
      for (const r of tbody.rows) {
        if (r === drag.row) continue;
        const b = r.getBoundingClientRect();
        if (y < b.top + b.height / 2) { target = r; break; }
      }
      if (target) { if (target !== drag.row.nextSibling) tbody.insertBefore(drag.row, target); }
      else if (tbody.lastElementChild !== drag.row) tbody.appendChild(drag.row);
      renumber();
      if (y < 70) window.scrollBy(0, -14);
      else if (y > window.innerHeight - 70) window.scrollBy(0, 14);
    });
    const drop = async () => {
      if (!drag) return;
      const row = drag.row, changed = ids() !== drag.before;
      row.classList.remove("dragging");
      drag = null;
      if (!changed) return;
      row.classList.add("saving");
      try {
        const r = await fetch("/queue/order", { method: "POST", body: new URLSearchParams({ ids: ids() }) });
        if (!r.ok) throw new Error(String(r.status));
        row.classList.remove("saving");
        row.classList.add("moved");
        setTimeout(() => row.classList.remove("moved"), 1200);
      } catch (err) {
        location.reload(); // show the order the server really has
      }
    };
    tbody.addEventListener("pointerup", drop);
    tbody.addEventListener("pointercancel", drop);
  }

  // ---------------------------------------------------------- "Look Who's Talking"
  // The audio is only the passage (media fragment #t=from,to). JS adds: play
  // again, click a line to hear it, the current line/word highlighted, and
  // "Someone else…" / the name buttons exclude each other.
  const qa = $("quiz-audio");
  if (qa) {
    const from = Number(qa.dataset.from) / 1000, to = Number(qa.dataset.to) / 1000;
    const qform = document.querySelector("form.quiz");
    const ws = Number(qform.querySelector("input[name=ws]").value);
    let rows = [], words = [];
    const refresh = () => {
      rows = Array.from(qform.querySelectorAll(".qrow"));
      words = Array.from(qform.querySelectorAll(".qrow .text span[data-s]"));
    };
    refresh();
    const replay = $("quiz-replay");
    if (replay) replay.hidden = false;
    const rowStop = qform.dataset.rowStop === "1"; // voice page: lines are far apart, stop after each
    let stopAt = 0;
    const playFrom = (sec) => { qa.currentTime = sec; qa.play(); };
    if (replay) replay.addEventListener("click", () => playFrom(from));
    let curRow = null, curW = null, qraf = 0;
    const qtime = () => {
      const t = qa.currentTime, ms = t * 1000;
      if (t >= to && !qa.paused) qa.pause(); // some browsers ignore the fragment end after seeking
      if (stopAt && t >= stopAt && !qa.paused) { qa.pause(); stopAt = 0; } // voice page: just this line
      const row = rows.find((r) => ms >= Number(r.dataset.s) - 150 && ms <= Number(r.dataset.e) + 150) || null;
      if (row !== curRow) { if (curRow) curRow.classList.remove("now"); if (row) row.classList.add("now"); curRow = row; }
      const w = words.find((x) => ms >= Number(x.dataset.s) && ms <= Number(x.dataset.e) + 150) || null;
      if (w !== curW) { if (curW) curW.classList.remove("w-now"); if (w) w.classList.add("w-now"); curW = w; }
    };
    const qtick = () => { qtime(); qraf = requestAnimationFrame(qtick); };
    qa.addEventListener("play", () => { if (qa.currentTime >= to - 0.05) qa.currentTime = from; cancelAnimationFrame(qraf); qraf = requestAnimationFrame(qtick); });
    qa.addEventListener("pause", () => { cancelAnimationFrame(qraf); qtime(); });

    const short = (ms) => { const s = Math.max(0, Math.floor(ms / 1000)); return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0"); };
    // field names carry the row number (s0, l0, x0 …): renumber after a split
    const renumber = () => {
      refresh();
      rows.forEach((r, i) => {
        r.querySelectorAll("[name]").forEach((el) => { el.name = el.name.replace(/^([selox])\d+$/, "$1" + i); });
      });
      qform.querySelector("input[name=n]").value = rows.length;
    };
    const setRange = (r, a, b) => {
      r.dataset.s = a; r.dataset.e = b;
      r.querySelector("input[name^=s]").value = a;
      r.querySelector("input[name^=e]").value = b;
      const ts = r.querySelector(".ts");
      ts.dataset.seek = a; ts.textContent = short(a - ws);
    };
    // the split marks: a thin bar between two words, where a click splits the
    // line (the speaker changes in the middle of a sentence). Shown when the
    // mouse is over the gap, always on a touch screen - see the CSS
    const addGaps = (row) => {
      const text = row.querySelector(".text");
      if (!text) return;
      text.querySelectorAll(".gap").forEach((g) => g.remove());
      Array.from(text.querySelectorAll("span[data-s]")).slice(1).forEach((w) => {
        const g = document.createElement("span");
        g.className = "gap";
        g.setAttribute("role", "button");
        g.title = "Split the line here - someone else starts speaking";
        w.before(g);
      });
    };
    // split the line before word w
    const split = (r, w) => {
      const ws_ = Array.from(r.querySelectorAll(".text span[data-s]"));
      const k = ws_.indexOf(w);
      if (k <= 0) return;
      r.querySelectorAll(".text .gap").forEach((g) => g.remove()); // not into the clone
      const radioChecked = (row) => { const c = row.querySelector("input[type=radio]:checked"); return c ? c.value : null; };
      const keep = radioChecked(r), other = r.querySelector("select").value;
      const nr = r.cloneNode(true);
      nr.classList.remove("now");
      r.after(nr);
      const text = (row) => row.querySelector(".text");
      // old row keeps words 0..k-1, new row gets k..end
      ws_.slice(k).forEach((x) => { x.nextSibling && x.nextSibling.nodeType === 3 && x.nextSibling.remove(); x.remove(); });
      Array.from(text(nr).querySelectorAll("span[data-s]")).slice(0, k).forEach((x) => { x.nextSibling && x.nextSibling.nodeType === 3 && x.nextSibling.remove(); x.remove(); });
      const endOld = Number(ws_[k - 1].dataset.e), oldEnd = Number(r.dataset.e);
      setRange(nr, Number(w.dataset.s), oldEnd);
      setRange(r, Number(r.dataset.s), endOld);
      renumber();
      // same choice as before in both halves (radio groups have new names now)
      [r, nr].forEach((row) => {
        row.querySelector("select").value = other;
        row.querySelectorAll("input[type=radio]").forEach((x) => { x.checked = other === "" && x.value === keep; });
        addGaps(row);
        bindRow(row);
      });
      nr.classList.add("split-new");
      nr.querySelector("input[type=radio]:not(:checked)")?.focus();
    };
    const bound = new WeakSet();
    const bindRow = (r) => {
      if (bound.has(r)) return;
      bound.add(r);
      const sel = r.querySelector("select");
      r.addEventListener("change", (e) => {
        if (e.target === sel) {
          r.classList.toggle("other", sel.value !== "");
          const radios = r.querySelectorAll("input[type=radio]");
          if (sel.value !== "") radios.forEach((x) => { x.checked = false; });
          else { const cur = r.querySelector("input[name^=o]").value; radios.forEach((x) => { x.checked = x.value === cur; }); }
        } else if (e.target.type === "radio") { sel.value = ""; r.classList.remove("other"); }
      });
      r.addEventListener("click", (e) => {
        const cp = e.target.closest(".ctx-play[data-from]");
        if (cp) {
          e.preventDefault();
          stopAt = Number(cp.dataset.to) / 1000 + 0.4;
          playFrom(Math.max(from, Number(cp.dataset.from) / 1000 - 0.3));
          return;
        }
        const ts = e.target.closest(".ts[data-seek]");
        if (ts) {
          e.preventDefault();
          stopAt = rowStop ? Number(r.dataset.e) / 1000 + 0.4 : 0;
          playFrom(Math.max(from, Number(ts.dataset.seek) / 1000 - 0.3));
          return;
        }
        const g = e.target.closest(".text .gap");
        if (g && g.nextElementSibling) split(r, g.nextElementSibling);
      });
    };
    rows.forEach(addGaps);
    rows.forEach(bindRow);
    const hint = $("quiz-split-hint");
    if (hint) hint.hidden = false;
  }

  // ---------------------------------------------------------- transcript + audio
  const audio = $("audio");
  if (!audio || !transcript) return;

  const lines = Array.from(transcript.querySelectorAll(".u"));
  const starts = lines.map((p) => Number(p.dataset.ms));
  const totalMs = lanes ? Number(lanes.dataset.total) : 0;
  const heads = lanes ? Array.from(lanes.querySelectorAll(".playhead")) : [];

  function seek(ms) {
    audio.currentTime = ms / 1000;
    audio.play();
  }

  // playback speed (remembered in this browser)
  const speed = $("speed");
  if (speed) {
    let saved = null;
    try { saved = lsGet("speed"); } catch (e) { /* storage blocked */ }
    if (saved && !isNaN(Number(saved))) {
      // nearest available speed (older versions had 0.5 steps)
      let best = null;
      Array.from(speed.options).forEach((o) => {
        if (best === null || Math.abs(Number(o.value) - saved) < Math.abs(Number(best.value) - saved)) best = o;
      });
      if (best) speed.value = best.value;
    }
    const apply = () => { audio.playbackRate = Number(speed.value); };
    apply();
    audio.addEventListener("loadedmetadata", apply);
    speed.addEventListener("change", () => {
      apply();
      try { lsSet("speed", speed.value); } catch (e) { /* ignore */ }
    });
  }

  // keys: left/right = 1 s (Shift: 5 s), space = play/pause - not while typing
  document.addEventListener("keydown", (e) => {
    const t = e.target;
    if (e.ctrlKey || e.altKey || e.metaKey) return;
    if (t.closest && t.closest("input, textarea, select, button, audio, [contenteditable]")) return;
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      const step = e.shiftKey ? 5 : 1;
      const d = e.key === "ArrowLeft" ? -step : step;
      audio.currentTime = Math.max(0, Math.min((audio.duration || Infinity), audio.currentTime + d));
      e.preventDefault();
    } else if (e.key === " ") {
      if (audio.paused) audio.play(); else audio.pause();
      e.preventDefault();
    }
  });

  transcript.addEventListener("click", (e) => {
    const ts = e.target.closest(".ts");
    if (!ts) return;
    e.preventDefault();
    seek(Math.floor(Number(ts.closest(".u").dataset.ms) / 1000) * 1000); // the shown second
  });

  if (lanes && totalMs > 0) {
    lanes.addEventListener("click", (e) => {
      const track = e.target.closest(".lane-track");
      if (!track) return;
      const r = track.getBoundingClientRect();
      const ms = ((e.clientX - r.left) / r.width) * totalMs;
      seek(Math.max(0, ms));
      // bring the matching line into view
      const i = indexAt(ms);
      if (i >= 0) lines[i].scrollIntoView({ block: "center", behavior: "smooth" });
    });
  }

  function indexAt(ms) {
    // last line starting at or before ms (binary search)
    let lo = 0, hi = starts.length - 1, ans = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (starts[mid] <= ms) { ans = mid; lo = mid + 1; } else hi = mid - 1;
    }
    return ans;
  }

  let current = null;
  // current word: all word spans in time order, found by binary search
  const wordEls = Array.from(transcript.querySelectorAll(".u .text span[data-s]"));
  const wordStarts = wordEls.map((w) => Number(w.dataset.s));
  let curWord = null;
  function wordAt(ms) {
    let lo = 0, hi = wordStarts.length - 1, ans = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (wordStarts[mid] <= ms) { ans = mid; lo = mid + 1; } else hi = mid - 1;
    }
    if (ans < 0) return null;
    const w = wordEls[ans];
    // between words (pause): keep the last one only for a moment
    return ms <= Number(w.dataset.e) + 600 ? w : null;
  }
  const nowBtn = document.querySelector('#pagenav [data-go="now"]');

  function onTime() {
    const ms = audio.currentTime * 1000;
    const i = indexAt(ms);
    const line = i >= 0 ? lines[i] : null;
    if (line !== current) {
      if (current) current.classList.remove("now");
      if (line) line.classList.add("now");
      current = line;
    }
    const w = wordAt(ms);
    if (w !== curWord) {
      if (curWord) curWord.classList.remove("w-now");
      if (w) w.classList.add("w-now");
      curWord = w;
    }
    if (totalMs > 0) {
      const pct = Math.min(100, (ms / totalMs) * 100) + "%";
      heads.forEach((h) => { h.hidden = false; h.style.left = pct; });
    }
  }
  audio.addEventListener("timeupdate", onTime);
  audio.addEventListener("seeked", onTime);
  // timeupdate only fires ~4x per second - smoother word following while playing
  let raf = 0;
  const tick = () => { onTime(); raf = requestAnimationFrame(tick); };
  audio.addEventListener("play", () => { if (nowBtn) nowBtn.hidden = false; cancelAnimationFrame(raf); raf = requestAnimationFrame(tick); });
  audio.addEventListener("pause", () => cancelAnimationFrame(raf));

  // link like /episodes/5#t123000 starts playback there
  const m = location.hash.match(/^#t(\d+)$/);
  if (m) {
    const i = indexAt(Number(m[1]));
    if (i >= 0) lines[i].scrollIntoView({ block: "center" });
    audio.addEventListener("loadedmetadata", () => { audio.currentTime = Number(m[1]) / 1000; }, { once: true });
  }
})();
