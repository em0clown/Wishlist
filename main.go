package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddress   = "127.0.0.1:8080"
	databaseFile     = "data/wishlist-db.json"
	sessionCookie    = "wishlist_session"
	sessionLifetime  = 7 * 24 * time.Hour
	passwordIter     = 120_000
	passwordSaltSize = 16
	passwordKeySize  = 32
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{3,24}$`)

type User struct {
	ID           int       `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	Token     string    `json:"token"`
	UserID    int       `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WishItem struct {
	ID        int       `json:"id"`
	UserID    int       `json:"user_id"`
	Name      string    `json:"name"`
	Price     float64   `json:"price"`
	ImageURL  string    `json:"image_url"`
	Link      string    `json:"link"`
	Priority  string    `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}

type Follow struct {
	FollowerID  int       `json:"follower_id"`
	FollowingID int       `json:"following_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type Database struct {
	NextUserID int        `json:"next_user_id"`
	NextItemID int        `json:"next_item_id"`
	Users      []User     `json:"users"`
	Sessions   []Session  `json:"sessions"`
	Items      []WishItem `json:"items"`
	Follows    []Follow   `json:"follows"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data Database
}

type UserSummary struct {
	Username    string
	ItemCount   int
	Total       float64
	IsFollowing bool
	IsSelf      bool
}

type PageData struct {
	CurrentUser User
	Profile     User
	Items       []WishItem
	Total       float64
	IsOwn       bool
	IsFollowing bool
	Following   []UserSummary
	Message     string
	Error       string
}

type UsersPageData struct {
	CurrentUser User
	Users       []UserSummary
	Following   []UserSummary
	Message     string
	Error       string
}

type AuthData struct {
	Mode  string
	Title string
	Error string
}

var (
	store     *Store
	templates *template.Template
)

func main() {
	var err error
	store, err = OpenStore(databaseFile)
	if err != nil {
		log.Fatalf("ошибка базы данных: %v", err)
	}

	templates = template.Must(template.New("").Funcs(template.FuncMap{
		"money":         formatMoney,
		"priorityLabel": priorityLabel,
		"initial":       initial,
		"profileURL":    profileURL,
		"date":          formatDate,
	}).ParseGlob("templates/*.html"))

	mux := http.NewServeMux()
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		staticHandler.ServeHTTP(w, r)
	}))

	mux.HandleFunc("/", dashboardHandler)
	mux.HandleFunc("/users", usersHandler)
	mux.HandleFunc("/u/", profileHandler)
	mux.HandleFunc("/login", loginHandler)
	mux.HandleFunc("/register", registerHandler)
	mux.HandleFunc("/logout", logoutHandler)
	mux.HandleFunc("/add", addItemHandler)
	mux.HandleFunc("/delete", deleteItemHandler)
	mux.HandleFunc("/follow", followHandler)
	mux.HandleFunc("/unfollow", unfollowHandler)

	addr := serverAddress()
	log.Printf("🚀 Сервер запущен на http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func serverAddress() string {
	if addr := strings.TrimSpace(os.Getenv("ADDR")); addr != "" {
		return addr
	}
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		if strings.Contains(port, ":") {
			return port
		}
		return "127.0.0.1:" + port
	}
	return defaultAddress
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, data: Database{NextUserID: 1, NextItemID: 1, Users: []User{}, Sessions: []Session{}, Items: []WishItem{}, Follows: []Follow{}}}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, s.saveLocked()
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if err := json.NewDecoder(file).Decode(&s.data); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	s.normalizeLocked()
	return s, nil
}

func (s *Store) normalizeLocked() {
	if s.data.Users == nil {
		s.data.Users = []User{}
	}
	if s.data.Sessions == nil {
		s.data.Sessions = []Session{}
	}
	if s.data.Items == nil {
		s.data.Items = []WishItem{}
	}
	if s.data.Follows == nil {
		s.data.Follows = []Follow{}
	}
	if s.data.NextUserID == 0 {
		s.data.NextUserID = maxUserID(s.data.Users) + 1
	}
	if s.data.NextItemID == 0 {
		s.data.NextItemID = maxItemID(s.data.Items) + 1
	}
}

func (s *Store) CreateUser(username, password string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return User{}, errors.New("ник: 3-24 символа, только латиница, цифры и _")
	}
	if len(password) < 6 {
		return User{}, errors.New("пароль должен быть не короче 6 символов")
	}
	for _, u := range s.data.Users {
		if strings.EqualFold(u.Username, username) {
			return User{}, errors.New("такой аккаунт уже существует")
		}
	}

	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}

	user := User{
		ID:           s.data.NextUserID,
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    time.Now(),
	}
	s.data.NextUserID++
	s.data.Users = append(s.data.Users, user)
	return user, s.saveLocked()
}

func (s *Store) Authenticate(username, password string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, user := range s.data.Users {
		if strings.EqualFold(user.Username, strings.TrimSpace(username)) {
			if verifyPassword(user.PasswordHash, password) {
				return user, nil
			}
			break
		}
	}
	return User{}, errors.New("неверный логин или пароль")
}

func (s *Store) CreateSession(userID int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	s.cleanupExpiredSessionsLocked()
	s.data.Sessions = append(s.data.Sessions, Session{
		Token:     token,
		UserID:    userID,
		ExpiresAt: time.Now().Add(sessionLifetime),
	})
	return token, s.saveLocked()
}

func (s *Store) UserBySession(token string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, session := range s.data.Sessions {
		if session.Token == token && session.ExpiresAt.After(now) {
			for _, user := range s.data.Users {
				if user.ID == session.UserID {
					return user, true
				}
			}
		}
	}
	return User{}, false
}

func (s *Store) UserByUsername(username string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	username = strings.TrimSpace(username)
	for _, user := range s.data.Users {
		if strings.EqualFold(user.Username, username) {
			return user, true
		}
	}
	return User{}, false
}

func (s *Store) DeleteSession(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.data.Sessions[:0]
	for _, session := range s.data.Sessions {
		if session.Token != token {
			filtered = append(filtered, session)
		}
	}
	s.data.Sessions = filtered
	return s.saveLocked()
}

func (s *Store) ItemsForUser(userID int) ([]WishItem, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := make([]WishItem, 0)
	total := 0.0
	for _, item := range s.data.Items {
		if item.UserID == userID {
			items = append(items, item)
			total += item.Price
		}
	}
	return items, total
}

func (s *Store) AddItem(userID int, name string, price float64, imageURL, link, priority string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("название не может быть пустым")
	}
	if price < 0 {
		return errors.New("цена не может быть отрицательной")
	}
	priority = normalizePriority(priority)

	item := WishItem{
		ID:        s.data.NextItemID,
		UserID:    userID,
		Name:      name,
		Price:     price,
		ImageURL:  strings.TrimSpace(imageURL),
		Link:      strings.TrimSpace(link),
		Priority:  priority,
		CreatedAt: time.Now(),
	}
	s.data.NextItemID++
	s.data.Items = append([]WishItem{item}, s.data.Items...)
	return s.saveLocked()
}

func (s *Store) DeleteItem(userID, itemID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.data.Items[:0]
	for _, item := range s.data.Items {
		if !(item.UserID == userID && item.ID == itemID) {
			filtered = append(filtered, item)
		}
	}
	s.data.Items = filtered
	return s.saveLocked()
}

func (s *Store) FollowUser(followerID, followingID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if followerID == followingID {
		return errors.New("нельзя подписаться на себя")
	}
	if !s.userExistsLocked(followerID) || !s.userExistsLocked(followingID) {
		return errors.New("пользователь не найден")
	}
	for _, f := range s.data.Follows {
		if f.FollowerID == followerID && f.FollowingID == followingID {
			return nil
		}
	}
	s.data.Follows = append(s.data.Follows, Follow{
		FollowerID:  followerID,
		FollowingID: followingID,
		CreatedAt:   time.Now(),
	})
	return s.saveLocked()
}

func (s *Store) UnfollowUser(followerID, followingID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := s.data.Follows[:0]
	for _, f := range s.data.Follows {
		if !(f.FollowerID == followerID && f.FollowingID == followingID) {
			filtered = append(filtered, f)
		}
	}
	s.data.Follows = filtered
	return s.saveLocked()
}

func (s *Store) IsFollowing(followerID, followingID int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.isFollowingLocked(followerID, followingID)
}

func (s *Store) Following(userID int) []UserSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]UserSummary, 0)
	for _, f := range s.data.Follows {
		if f.FollowerID != userID {
			continue
		}
		if user, ok := s.userByIDLocked(f.FollowingID); ok {
			result = append(result, s.summaryForUserLocked(user, userID))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Username) < strings.ToLower(result[j].Username)
	})
	return result
}

func (s *Store) UserSummaries(currentUserID int) []UserSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]UserSummary, 0, len(s.data.Users))
	for _, user := range s.data.Users {
		if user.ID == currentUserID {
			continue
		}
		result = append(result, s.summaryForUserLocked(user, currentUserID))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsFollowing != result[j].IsFollowing {
			return result[i].IsFollowing
		}
		return strings.ToLower(result[i].Username) < strings.ToLower(result[j].Username)
	})
	return result
}

func (s *Store) userExistsLocked(userID int) bool {
	_, ok := s.userByIDLocked(userID)
	return ok
}

func (s *Store) userByIDLocked(userID int) (User, bool) {
	for _, user := range s.data.Users {
		if user.ID == userID {
			return user, true
		}
	}
	return User{}, false
}

func (s *Store) isFollowingLocked(followerID, followingID int) bool {
	for _, f := range s.data.Follows {
		if f.FollowerID == followerID && f.FollowingID == followingID {
			return true
		}
	}
	return false
}

func (s *Store) summaryForUserLocked(user User, currentUserID int) UserSummary {
	total := 0.0
	count := 0
	for _, item := range s.data.Items {
		if item.UserID == user.ID {
			count++
			total += item.Price
		}
	}
	return UserSummary{
		Username:    user.Username,
		ItemCount:   count,
		Total:       total,
		IsFollowing: s.isFollowingLocked(currentUserID, user.ID),
		IsSelf:      currentUserID == user.ID,
	}
}

func (s *Store) cleanupExpiredSessionsLocked() {
	now := time.Now()
	filtered := s.data.Sessions[:0]
	for _, session := range s.data.Sessions {
		if session.ExpiresAt.After(now) {
			filtered = append(filtered, session)
		}
	}
	s.data.Sessions = filtered
}

func (s *Store) saveLocked() error {
	s.normalizeLocked()
	tmpPath := s.path + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	user, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	renderWishlistPage(w, r, user, user)
}

func profileHandler(w http.ResponseWriter, r *http.Request) {
	current, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	username := strings.Trim(strings.TrimPrefix(r.URL.Path, "/u/"), "/")
	if username == "" {
		http.Redirect(w, r, "/users", http.StatusSeeOther)
		return
	}
	username, _ = url.PathUnescape(username)
	profile, ok := store.UserByUsername(username)
	if !ok {
		http.NotFound(w, r)
		return
	}
	renderWishlistPage(w, r, current, profile)
}

func renderWishlistPage(w http.ResponseWriter, r *http.Request, current, profile User) {
	items, total := store.ItemsForUser(profile.ID)
	data := PageData{
		CurrentUser: current,
		Profile:     profile,
		Items:       items,
		Total:       total,
		IsOwn:       current.ID == profile.ID,
		IsFollowing: store.IsFollowing(current.ID, profile.ID),
		Following:   store.Following(current.ID),
		Message:     r.URL.Query().Get("message"),
		Error:       r.URL.Query().Get("error"),
	}
	render(w, "index.html", data)
}

func usersHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	render(w, "users.html", UsersPageData{
		CurrentUser: user,
		Users:       store.UserSummaries(user.ID),
		Following:   store.Following(user.ID),
		Message:     r.URL.Query().Get("message"),
		Error:       r.URL.Query().Get("error"),
	})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		render(w, "auth.html", AuthData{Mode: "login", Title: "Вход"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, err := store.Authenticate(r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		render(w, "auth.html", AuthData{Mode: "login", Title: "Вход", Error: err.Error()})
		return
	}
	setSession(w, user.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		render(w, "auth.html", AuthData{Mode: "register", Title: "Регистрация"})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, err := store.CreateUser(r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		render(w, "auth.html", AuthData{Mode: "register", Title: "Регистрация", Error: err.Error()})
		return
	}
	setSession(w, user.ID)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = store.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func addItemHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	price, err := strconv.ParseFloat(strings.ReplaceAll(r.FormValue("price"), ",", "."), 64)
	if err != nil {
		http.Redirect(w, r, "/?error=Некорректная+цена", http.StatusSeeOther)
		return
	}

	err = store.AddItem(user.ID, r.FormValue("name"), price, r.FormValue("image_url"), r.FormValue("link"), r.FormValue("priority"))
	if err != nil {
		http.Redirect(w, r, "/?error="+urlQueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/?message=Желание+добавлено", http.StatusSeeOther)
}

func deleteItemHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err == nil {
		_ = store.DeleteItem(user.ID, id)
	}
	http.Redirect(w, r, "/?message=Желание+удалено", http.StatusSeeOther)
}

func followHandler(w http.ResponseWriter, r *http.Request) {
	changeFollow(w, r, true)
}

func unfollowHandler(w http.ResponseWriter, r *http.Request) {
	changeFollow(w, r, false)
}

func changeFollow(w http.ResponseWriter, r *http.Request, follow bool) {
	current, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/users", http.StatusSeeOther)
		return
	}

	target, ok := store.UserByUsername(r.FormValue("username"))
	if !ok {
		http.Redirect(w, r, safeNext(r, "/users")+queryJoin("error", "Пользователь не найден"), http.StatusSeeOther)
		return
	}

	var err error
	message := "Подписка оформлена"
	if follow {
		err = store.FollowUser(current.ID, target.ID)
	} else {
		err = store.UnfollowUser(current.ID, target.ID)
		message = "Подписка отменена"
	}
	if err != nil {
		http.Redirect(w, r, safeNext(r, profileURL(target.Username))+queryJoin("error", err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, safeNext(r, profileURL(target.Username))+queryJoin("message", message), http.StatusSeeOther)
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func currentUser(r *http.Request) (User, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return User{}, false
	}
	return store.UserBySession(cookie.Value)
}

func setSession(w http.ResponseWriter, userID int) {
	token, err := store.CreateSession(userID)
	if err != nil {
		log.Printf("не удалось создать сессию: %v", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(sessionLifetime),
		MaxAge:   int(sessionLifetime.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2Key([]byte(password), salt, passwordIter, passwordKeySize)
	return fmt.Sprintf("pbkdf2$%d$%s$%s",
		passwordIter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func verifyPassword(storedHash, password string) bool {
	parts := strings.SplitN(storedHash, "$", 4)
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actual := pbkdf2Key([]byte(password), salt, iter, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	var output []byte

	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		var counter [4]byte
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		mac.Write(counter[:])
		u := mac.Sum(nil)
		t := make([]byte, hLen)
		copy(t, u)

		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := 0; j < hLen; j++ {
				t[j] ^= u[j]
			}
		}
		output = append(output, t...)
	}
	return output[:keyLen]
}

func randomToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func normalizePriority(priority string) string {
	switch priority {
	case "high", "medium", "low":
		return priority
	default:
		return "medium"
	}
}

func priorityLabel(priority string) string {
	switch priority {
	case "high":
		return "Высокий"
	case "low":
		return "Низкий"
	default:
		return "Средний"
	}
}

func formatMoney(value float64) string {
	return fmt.Sprintf("%.0f ₽", value)
}

func initial(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return "?"
	}
	return strings.ToUpper(string(runes[0]))
}

func profileURL(username string) string {
	return "/u/" + url.PathEscape(username)
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format("02.01.2006")
}

func safeNext(r *http.Request, fallback string) string {
	next := strings.TrimSpace(r.FormValue("next"))
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return fallback
	}
	return next
}

func queryJoin(key, value string) string {
	if value == "" {
		return ""
	}
	return "?" + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}

func maxUserID(users []User) int {
	maxID := 0
	for _, user := range users {
		if user.ID > maxID {
			maxID = user.ID
		}
	}
	return maxID
}

func maxItemID(items []WishItem) int {
	maxID := 0
	for _, item := range items {
		if item.ID > maxID {
			maxID = item.ID
		}
	}
	return maxID
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(value)
}
