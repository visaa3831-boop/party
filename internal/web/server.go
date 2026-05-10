package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/sessions"

	"partydiscord/internal/backup"
	"partydiscord/internal/config"
)

const sessionName = "party_discord"

type oauthUser struct {
	ID            string  `json:"id"`
	Username      string  `json:"username"`
	GlobalName    *string `json:"global_name"`
	Discriminator string  `json:"discriminator"`
	Avatar        *string `json:"avatar"`
}

func NewHandler(cfg *config.Config) http.Handler {
	store := sessions.NewCookieStore([]byte(cfg.SessionSecret))
	store.Options.HttpOnly = true
	store.Options.Secure = false // set true behind HTTPS + SameSite
	store.Options.SameSite = http.SameSiteLaxMode
	store.Options.MaxAge = 86400 * 7

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		handleHome(w, r, store, cfg)
	})
	mux.HandleFunc("/backup", func(w http.ResponseWriter, r *http.Request) {
		handleBackupForm(w, r, store)
	})
	mux.HandleFunc("/backup/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleBackupExport(w, r, store, cfg)
	})
	mux.HandleFunc("/auth/discord", func(w http.ResponseWriter, r *http.Request) {
		handleAuthStart(w, r, store, cfg)
	})
	mux.HandleFunc("/auth/discord/callback", func(w http.ResponseWriter, r *http.Request) {
		handleAuthCallback(w, r, store, cfg)
	})
	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		handleLogout(w, r, store)
	})
	return mux
}

func getSess(store *sessions.CookieStore, r *http.Request) (*sessions.Session, error) {
	return store.Get(r, sessionName)
}

func handleHome(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, _ *config.Config) {
	sess, err := getSess(store, r)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	raw, ok := sess.Values["user_json"].(string)
	if !ok || raw == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Discord sign-in</title></head><body><p><a href="/auth/discord">Sign in with Discord</a></p></body></html>`)
		return
	}
	var u oauthUser
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		http.Error(w, "bad session", http.StatusInternalServerError)
		return
	}
	name := u.Username
	if u.GlobalName != nil && *u.GlobalName != "" {
		name = *u.GlobalName
	}
	t := template.Must(template.New("h").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Signed in</title></head>
<body>
  <p>Signed in as <strong>{{.Name}}</strong> (<code>{{.ID}}</code>).</p>
  <p><a href="/backup">Download full server backup (JSON)</a></p>
  <p><a href="/logout">Sign out</a></p>
</body></html>`))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = t.Execute(w, struct {
		Name string
		ID   string
	}{Name: name, ID: u.ID})
}

func handleBackupForm(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore) {
	sess, err := getSess(store, r)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if _, ok := sess.Values["user_json"].(string); !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Server backup</title></head>
<body>
  <p>Full export: structure, messages (all accessible channels/threads), emojis, stickers, webhooks, invites, bans, scheduled events, auto-mod, integrations, optional members.</p>
  <p>You must <strong>own</strong> the server. The bot must be in the server with permission to read content (Administrator is easiest). Large servers can take a long time.</p>
  <form method="post" action="/backup/export">
    <label>Guild ID <input name="guildId" required pattern="[0-9]{17,20}" style="width:22rem"></label>
    <button type="submit">Download JSON</button>
  </form>
  <p><a href="/">Home</a></p>
</body></html>`)
}

func handleBackupExport(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, cfg *config.Config) {
	sess, err := getSess(store, r)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	raw, ok := sess.Values["user_json"].(string)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	var user oauthUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		http.Error(w, "bad session", http.StatusInternalServerError)
		return
	}
	if strings.TrimSpace(cfg.BotToken) == "" {
		http.Error(w, "DISCORD_BOT_TOKEN is not set", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	guildID := strings.TrimSpace(r.FormValue("guildId"))
	if len(guildID) < 17 || len(guildID) > 20 {
		http.Error(w, "invalid guild id", http.StatusBadRequest)
		return
	}

	dg, err := discordgo.New("Bot " + cfg.BotToken)
	if err != nil {
		http.Error(w, "bot session", http.StatusInternalServerError)
		return
	}

	guild, err := dg.Guild(guildID)
	if err != nil {
		http.Error(w, "guild not found or bot not in server", http.StatusNotFound)
		return
	}
	if guild.OwnerID != user.ID {
		http.Error(w, "only the server owner can export", http.StatusForbidden)
		return
	}

	payload, err := backup.Build(dg, guildID, backup.Options{
		SkipMembers:  cfg.SkipMembers,
		SkipMessages: cfg.SkipMessages,
		Logf:         nil,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("backup failed: %v", err), http.StatusInternalServerError)
		return
	}

	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("guild-%s-full-backup-%s.json", guildID, stamp)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(out)))
	_, _ = w.Write(out)
}

func handleAuthStart(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, cfg *config.Config) {
	sess, err := getSess(store, r)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	state, err := randomHex(16)
	if err != nil {
		http.Error(w, "state", http.StatusInternalServerError)
		return
	}
	sess.Values["oauth_state"] = state
	if err := sess.Save(r, w); err != nil {
		http.Error(w, "session save", http.StatusInternalServerError)
		return
	}
	q := url.Values{}
	q.Set("client_id", cfg.DiscordClientID)
	q.Set("redirect_uri", cfg.DiscordRedirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "identify")
	q.Set("state", state)
	http.Redirect(w, r, "https://discord.com/api/oauth2/authorize?"+q.Encode(), http.StatusFound)
}

func handleAuthCallback(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore, cfg *config.Config) {
	sess, err := getSess(store, r)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		http.Error(w, "discord: "+errMsg, http.StatusBadRequest)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	want, _ := sess.Values["oauth_state"].(string)
	delete(sess.Values, "oauth_state")
	if code == "" || state == "" || want == "" || state != want {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	form := url.Values{}
	form.Set("client_id", cfg.DiscordClientID)
	form.Set("client_secret", cfg.DiscordClientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", cfg.DiscordRedirectURI)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://discord.com/api/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "token request build", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "token request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "token exchange failed", http.StatusInternalServerError)
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		http.Error(w, "token parse", http.StatusInternalServerError)
		return
	}

	ureq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://discord.com/api/users/@me", nil)
	if err != nil {
		http.Error(w, "user request", http.StatusInternalServerError)
		return
	}
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := http.DefaultClient.Do(ureq)
	if err != nil {
		http.Error(w, "user fetch", http.StatusBadGateway)
		return
	}
	defer uresp.Body.Close()
	ubody, _ := io.ReadAll(uresp.Body)
	if uresp.StatusCode != http.StatusOK {
		http.Error(w, "user profile failed", http.StatusInternalServerError)
		return
	}
	var u oauthUser
	if err := json.Unmarshal(ubody, &u); err != nil {
		http.Error(w, "user parse", http.StatusInternalServerError)
		return
	}
	ub, _ := json.Marshal(u)
	sess.Values["user_json"] = string(ub)
	if err := sess.Save(r, w); err != nil {
		http.Error(w, "session save", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func handleLogout(w http.ResponseWriter, r *http.Request, store *sessions.CookieStore) {
	sess, err := getSess(store, r)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	sess.Options.MaxAge = -1
	if err := sess.Save(r, w); err != nil {
		http.Error(w, "session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
