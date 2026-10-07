package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// Invite links let an admin add a helper without making up a password for
// them: the admin fixes the podcasts the helper may edit when the invite is
// created, the helper only picks a username and a password. The QR code is
// nothing more than the invite URL rendered as a scannable image – it does
// not itself carry any rights, those live in the invites table server-side
// and can't be changed by whoever opens the link.

const inviteTTL = 7 * 24 * time.Hour

var inviteSchema = []string{
	`CREATE TABLE invites(
		id         INTEGER PRIMARY KEY,
		token_hash TEXT NOT NULL UNIQUE,
		created_by INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		created_at INTEGER NOT NULL,
		expires    INTEGER NOT NULL,
		used_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
		used_at    INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE invite_podcasts(
		invite_id INTEGER NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
		feed_id   INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		PRIMARY KEY(invite_id, feed_id))`,
}

type Invite struct {
	ID        int64
	CreatedBy string
	CreatedAt time.Time
	Expires   time.Time
	Podcasts  map[int64]bool
	Used      bool
}

func (iv Invite) Expired() bool { return time.Now().After(iv.Expires) }

// NewInvite creates an invite good for the given podcasts and returns the
// one-time token (never stored in clear – only its hash is).
func (s *Store) NewInvite(createdBy int64, feeds []int64) (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO invites(token_hash,created_by,created_at,expires) VALUES(?,?,?,?)`,
		tokenHash(tok), createdBy, time.Now().Unix(), time.Now().Add(inviteTTL).Unix())
	if err != nil {
		return "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	for _, f := range feeds {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO invite_podcasts(invite_id,feed_id) VALUES(?,?)`, id, f); err != nil {
			return "", err
		}
	}
	return tok, tx.Commit()
}

// Invites lists open (unused, unexpired) invites, newest first.
func (s *Store) Invites() ([]Invite, error) {
	rows, err := s.db.Query(`SELECT i.id, u.name, i.created_at, i.expires
		FROM invites i JOIN users u ON u.id=i.created_by
		WHERE i.used_by IS NULL AND i.expires > ? ORDER BY i.created_at DESC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		var iv Invite
		var created, expires int64
		if err := rows.Scan(&iv.ID, &iv.CreatedBy, &created, &expires); err != nil {
			continue
		}
		iv.CreatedAt, iv.Expires = time.Unix(created, 0), time.Unix(expires, 0)
		out = append(out, iv)
	}
	for i := range out {
		out[i].Podcasts, _ = s.invitePodcasts(out[i].ID)
	}
	return out, nil
}

func (s *Store) invitePodcasts(id int64) (map[int64]bool, error) {
	rows, err := s.db.Query(`SELECT feed_id FROM invite_podcasts WHERE invite_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int64]bool{}
	for rows.Next() {
		var f int64
		rows.Scan(&f)
		m[f] = true
	}
	return m, nil
}

// inviteByToken looks up an open, unexpired invite by its plain token (as
// carried in the URL). A used or expired invite is reported as not found –
// same message either way, so a stale link doesn't leak whether it was ever
// valid.
func (s *Store) inviteByToken(tok string) (*Invite, error) {
	var iv Invite
	var id, createdBy, created, expires, usedAt int64
	err := s.db.QueryRow(`SELECT id, created_by, created_at, expires, used_at FROM invites WHERE token_hash=?`,
		tokenHash(tok)).Scan(&id, &createdBy, &created, &expires, &usedAt)
	if err != nil {
		return nil, err
	}
	iv.ID, iv.CreatedAt, iv.Expires = id, time.Unix(created, 0), time.Unix(expires, 0)
	if usedAt != 0 || iv.Expired() {
		return nil, fmt.Errorf("invite not open")
	}
	iv.Podcasts, _ = s.invitePodcasts(id)
	return &iv, nil
}

func (s *Store) RevokeInvite(id, adminID int64) error {
	_, err := s.db.Exec(`DELETE FROM invites WHERE id=? AND used_by IS NULL`, id)
	return err
}

// AcceptInvite creates the helper's account with the invite's podcasts and
// consumes the invite in the same transaction, so a token can't be reused
// by a second tab racing the first.
func (s *Store) AcceptInvite(tok, name, pw string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id, expires, usedAt int64
	if err := tx.QueryRow(`SELECT id, expires, used_at FROM invites WHERE token_hash=?`, tokenHash(tok)).
		Scan(&id, &expires, &usedAt); err != nil {
		return 0, fmt.Errorf("this invite link isn't valid")
	}
	if usedAt != 0 || time.Now().After(time.Unix(expires, 0)) {
		return 0, fmt.Errorf("this invite link isn't open anymore")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, fmt.Errorf("the user needs a name")
	}
	if err := validPassword(pw); err != nil {
		return 0, err
	}
	h, err := hashPassword(pw)
	if err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO users(name,pass_hash,role,created_at) VALUES(?,?,?,?)`,
		name, h, roleEditor, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, fmt.Errorf("a user called %q exists already", name)
		}
		return 0, err
	}
	uid, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	rows, _ := tx.Query(`SELECT feed_id FROM invite_podcasts WHERE invite_id=?`, id)
	var feeds []int64
	for rows.Next() {
		var f int64
		rows.Scan(&f)
		feeds = append(feeds, f)
	}
	rows.Close()
	for _, f := range feeds {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_podcasts(user_id,feed_id) VALUES(?,?)`, uid, f); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`UPDATE invites SET used_by=?, used_at=? WHERE id=?`, uid, time.Now().Unix(), id); err != nil {
		return 0, err
	}
	return uid, tx.Commit()
}

// ---------------------------------------------------------------- web

func (s *Server) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var feeds []int64
	for _, v := range r.Form["podcast"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			feeds = append(feeds, id)
		}
	}
	tok, err := s.st.NewInvite(currentUser(r).ID, feeds)
	if err != nil {
		back(w, r, "/users", "", err.Error())
		return
	}
	logf("Invite created by %s for %d podcast(s)", currentUser(r).Name, len(feeds))
	http.Redirect(w, r, "/users?invited="+url.QueryEscape(tok), http.StatusSeeOther)
}

func (s *Server) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.st.RevokeInvite(id, currentUser(r).ID)
	back(w, r, "/users", "Invite revoked.", "")
}

func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	iv, err := s.st.inviteByToken(tok)
	if err != nil {
		s.render(w, r, "join", "Join", "", map[string]any{"Gone": true})
		return
	}
	feeds, _ := s.st.Feeds()
	var names []string
	for _, f := range feeds {
		if iv.Podcasts[f.ID] {
			names = append(names, f.Title)
		}
	}
	sort.Strings(names)
	s.render(w, r, "join", "Join", "", map[string]any{
		"Token": tok, "Podcasts": names, "Name": r.URL.Query().Get("name"),
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	name, pw := r.FormValue("name"), r.FormValue("password")
	uid, err := s.st.AcceptInvite(tok, name, pw)
	if err != nil {
		q := url.Values{"err": {err.Error()}, "name": {name}}
		http.Redirect(w, r, "/join/"+url.PathEscape(tok)+"?"+q.Encode(), http.StatusSeeOther)
		return
	}
	stok, err := s.st.NewSession(uid)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: stok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), Expires: time.Now().Add(sessionTTL)})
	s.st.Audit(uid, actUserAdmin, 0, 0, 0, "joined via invite")
	logf("Joined via invite: %s", name)
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// inviteQR renders the invite link as an inline SVG QR code (medium error
// recovery – it only ever needs to survive a phone screen or a printout,
// not weather). No JavaScript, no external request; the browser gets the
// finished picture.
func inviteQR(link string) template.HTML {
	q, err := qrcode.New(link, qrcode.Medium)
	if err != nil {
		return ""
	}
	q.DisableBorder = true
	bits := q.Bitmap()
	n := len(bits)
	const quiet = 4 // modules of white margin, per the QR spec
	dim := n + quiet*2
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="QR code for the invite link" shape-rendering="crispEdges">`, dim, dim)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/>`, dim, dim)
	b.WriteString(`<path fill="#000" d="`)
	for y, row := range bits {
		for x, dark := range row {
			if dark {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+quiet, y+quiet)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return template.HTML(b.String())
}
