package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// ErrNotFound is returned when a lookup finds no row, or when a row exists but
// the caller is not allowed to see it. The two are deliberately the same error:
// telling a user "that comic exists but is not yours" leaks the library.
var ErrNotFound = errors.New("not found")

// Comic sources. A library comic is server-wide by definition; an upload
// belongs to the user who made it. A claimed comic is a library comic an admin
// took into their own library: its file still lives under the library root and
// its path is still relative to it, but it has an owner and so is no longer
// server-wide. Claiming is what makes a comic dropped into the watched folder
// private to one person without moving the file.
const (
	SourceLibrary = "library"
	SourceUpload  = "upload"
	SourceClaimed = "claimed"
)

// --- Users ---

// CountUsers returns the number of registered users.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser inserts a user.
func (s *Store) CreateUser(id, name string, isAdmin bool) (api.User, error) {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO users(id,name,is_admin,created_at) VALUES(?,?,?,?)`,
		id, name, boolInt(isAdmin), now)
	if err != nil {
		return api.User{}, err
	}
	return api.User{ID: id, Name: name, IsAdmin: isAdmin, CreatedAt: now}, nil
}

// GetUser looks up a user by id.
func (s *Store) GetUser(id string) (api.User, error) {
	var u api.User
	var admin int
	err := s.db.QueryRow(`SELECT id,name,is_admin,created_at FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Name, &admin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.IsAdmin = admin != 0
	return u, err
}

// UserByName looks up a user by their display name.
func (s *Store) UserByName(name string) (api.User, error) {
	var u api.User
	var admin int
	err := s.db.QueryRow(`SELECT id,name,is_admin,created_at FROM users WHERE name=?`, name).
		Scan(&u.ID, &u.Name, &admin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	u.IsAdmin = admin != 0
	return u, err
}

// ListUsers returns all users ordered by creation.
func (s *Store) ListUsers() ([]api.User, error) {
	rows, err := s.db.Query(`SELECT id,name,is_admin,created_at FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []api.User
	for rows.Next() {
		var u api.User
		var admin int
		if err := rows.Scan(&u.ID, &u.Name, &admin, &u.CreatedAt); err != nil {
			return nil, err
		}
		u.IsAdmin = admin != 0
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountAdmins returns the number of admin users.
func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin=1`).Scan(&n)
	return n, err
}

// SetUserAdmin grants or revokes admin rights.
func (s *Store) SetUserAdmin(id string, isAdmin bool) error {
	res, err := s.db.Exec(`UPDATE users SET is_admin=? WHERE id=?`, boolInt(isAdmin), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteUser removes a user; credentials, sessions, uploads, collections and
// progress cascade via FK.
func (s *Store) DeleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Credentials ---

// StoredCredential is a passkey row, decoded from the DB.
type StoredCredential struct {
	ID         string
	UserID     string
	CredID     []byte
	PublicKey  []byte
	SignCount  uint32
	Transports []string
	AAGUID     []byte
	Name       string
	// BackupEligible/BackupState mirror the WebAuthn BE/BS flags captured at
	// registration. go-webauthn re-checks BE on every login and rejects a
	// mismatch, so they must round-trip through the DB or every passkey stops
	// working the moment it is stored without them.
	BackupEligible bool
	BackupState    bool
	LastUsed       int64
	CreatedAt      int64
}

const credCols = `id,user_id,cred_id,public_key,sign_count,transports,aaguid,name,backup_eligible,backup_state,last_used,created_at`

// AddCredential stores a new passkey for a user.
func (s *Store) AddCredential(c StoredCredential) error {
	_, err := s.db.Exec(`INSERT INTO credentials(`+credCols+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.UserID, c.CredID, c.PublicKey, c.SignCount,
		strings.Join(c.Transports, ","), c.AAGUID, c.Name,
		boolInt(c.BackupEligible), boolInt(c.BackupState), c.LastUsed, c.CreatedAt)
	return err
}

// CredentialsByUser returns all passkeys for a user.
func (s *Store) CredentialsByUser(userID string) ([]StoredCredential, error) {
	rows, err := s.db.Query(`SELECT `+credCols+` FROM credentials WHERE user_id=? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCreds(rows)
}

// CredentialByCredID looks up a passkey by its raw credential id.
func (s *Store) CredentialByCredID(credID []byte) (StoredCredential, error) {
	rows, err := s.db.Query(`SELECT `+credCols+` FROM credentials WHERE cred_id=?`, credID)
	if err != nil {
		return StoredCredential{}, err
	}
	defer rows.Close()
	creds, err := scanCreds(rows)
	if err != nil {
		return StoredCredential{}, err
	}
	if len(creds) == 0 {
		return StoredCredential{}, ErrNotFound
	}
	return creds[0], nil
}

// UpdateSignCount persists an incremented authenticator sign counter. A counter
// that fails to advance is how a cloned authenticator gives itself away, which
// only works if the last value survives the process.
func (s *Store) UpdateSignCount(credID []byte, count uint32) error {
	_, err := s.db.Exec(`UPDATE credentials SET sign_count=?, last_used=? WHERE cred_id=?`,
		count, time.Now().Unix(), credID)
	return err
}

// DeleteCredential removes one passkey from a user's account.
func (s *Store) DeleteCredential(userID, id string) error {
	res, err := s.db.Exec(`DELETE FROM credentials WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanCreds(rows *sql.Rows) ([]StoredCredential, error) {
	var out []StoredCredential
	for rows.Next() {
		var c StoredCredential
		var transports string
		if err := rows.Scan(&c.ID, &c.UserID, &c.CredID, &c.PublicKey, &c.SignCount,
			&transports, &c.AAGUID, &c.Name, &c.BackupEligible, &c.BackupState,
			&c.LastUsed, &c.CreatedAt); err != nil {
			return nil, err
		}
		if transports != "" {
			c.Transports = strings.Split(transports, ",")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- Invites ---

// CreateInvite stores a single-use invite. forUser binds a recovery invite to an
// existing user; pass "" for a normal (new-user) invite.
func (s *Store) CreateInvite(token, createdBy, forUser string, isAdmin bool, expiresAt int64) error {
	var cb, fu any
	if createdBy != "" {
		cb = createdBy
	}
	if forUser != "" {
		fu = forUser
	}
	_, err := s.db.Exec(`INSERT INTO invites(token,created_by,for_user,is_admin,expires_at,used_at,created_at)
		VALUES(?,?,?,?,?,0,?)`, token, cb, fu, boolInt(isAdmin), expiresAt, time.Now().Unix())
	return err
}

// InviteRow is a stored invite.
type InviteRow struct {
	Token     string
	IsAdmin   bool
	ExpiresAt int64
	UsedAt    int64
	CreatedAt int64
	CreatedBy string
	ForUser   string // bound target user id for recovery invites, else ""
}

const inviteCols = `token,is_admin,expires_at,used_at,created_at,created_by,for_user`

// GetInvite returns an invite by token.
func (s *Store) GetInvite(token string) (InviteRow, error) {
	rows, err := s.db.Query(`SELECT `+inviteCols+` FROM invites WHERE token=?`, token)
	if err != nil {
		return InviteRow{}, err
	}
	defer rows.Close()
	out, err := scanInvites(rows)
	if err != nil {
		return InviteRow{}, err
	}
	if len(out) == 0 {
		return InviteRow{}, ErrNotFound
	}
	return out[0], nil
}

// ConsumeInvite marks an invite used only if currently unused and unexpired.
// The check and the write are one statement so two concurrent enrollments on the
// same link cannot both see it as unused; the loser gets ErrNotFound.
func (s *Store) ConsumeInvite(token string) error {
	now := time.Now().Unix()
	res, err := s.db.Exec(`UPDATE invites SET used_at=? WHERE token=? AND used_at=0 AND expires_at>?`,
		now, token, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteInvite removes an invite by token (used to revoke a pending invite).
func (s *Store) DeleteInvite(token string) error {
	res, err := s.db.Exec(`DELETE FROM invites WHERE token=?`, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListPendingInvites returns unused, unexpired invites.
func (s *Store) ListPendingInvites() ([]InviteRow, error) {
	rows, err := s.db.Query(`SELECT `+inviteCols+` FROM invites
		WHERE used_at=0 AND expires_at>? ORDER BY created_at DESC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInvites(rows)
}

func scanInvites(rows *sql.Rows) ([]InviteRow, error) {
	var out []InviteRow
	for rows.Next() {
		var r InviteRow
		var admin int
		var createdBy, forUser sql.NullString
		if err := rows.Scan(&r.Token, &admin, &r.ExpiresAt, &r.UsedAt, &r.CreatedAt, &createdBy, &forUser); err != nil {
			return nil, err
		}
		r.IsAdmin = admin != 0
		r.CreatedBy = createdBy.String
		r.ForUser = forUser.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- Sessions ---

// CreateSession stores a session token.
func (s *Store) CreateSession(token, userID string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO sessions(token,user_id,expires_at,created_at) VALUES(?,?,?,?)`,
		token, userID, expiresAt, time.Now().Unix())
	return err
}

// SessionUser returns the user for a valid session token. Expiry is part of the
// lookup rather than a check on the result, so there is no window in which a
// caller can forget to apply it.
func (s *Store) SessionUser(token string) (api.User, error) {
	var u api.User
	var admin int
	err := s.db.QueryRow(`SELECT u.id,u.name,u.is_admin,u.created_at
		FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token=? AND s.expires_at>?`, token, time.Now().Unix()).
		Scan(&u.ID, &u.Name, &admin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.User{}, ErrNotFound
	}
	u.IsAdmin = admin != 0
	return u, err
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token=?`, token)
	return err
}

// DeleteUserSessions removes every session belonging to a user. This is what
// makes the long SessionTTL defensible: without it, the only way to cut off a
// device holding a live cookie is to delete the account it belongs to.
func (s *Store) DeleteUserSessions(userID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=?`, userID)
	return err
}

// DeleteUserSessionsExcept removes a user's sessions apart from keepToken. A
// user retiring a lost device must not be logged out of the device they are
// retiring it from, which is the difference between this and
// DeleteUserSessions.
//
// An empty keepToken matches no row and so revokes everything, which is the
// right answer for a caller that holds no session of its own to preserve.
//
// The count of revoked sessions is returned because "signed out 3 other
// devices" is the only feedback the user gets that the button did anything —
// nothing else about their own session changes.
func (s *Store) DeleteUserSessionsExcept(userID, keepToken string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE user_id=? AND token<>?`, userID, keepToken)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteExpiredSessions prunes sessions past their TTL.
func (s *Store) DeleteExpiredSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at<=?`, time.Now().Unix())
	return err
}

// --- API tokens ---

// CreateAPIToken stores a token bound to a user. tokenHash is the SHA-256 of the
// secret, never the secret itself — the caller keeps the only copy of the plain
// token, and the row is enough to authenticate it without being enough to forge
// it if the database leaks.
func (s *Store) CreateAPIToken(id, userID, name, tokenHash string) error {
	_, err := s.db.Exec(`INSERT INTO api_tokens(id,user_id,name,token_hash,created_at)
		VALUES(?,?,?,?,?)`, id, userID, name, tokenHash, time.Now().Unix())
	return err
}

// APITokenUser resolves a token hash to its user and stamps last_used. Unlike a
// session, an API token has no expiry: it lives until the user revokes it, which
// is why it is stored hashed. Returns ErrNotFound when no token matches.
func (s *Store) APITokenUser(tokenHash string) (api.User, error) {
	var u api.User
	var admin int
	err := s.db.QueryRow(`SELECT u.id,u.name,u.is_admin,u.created_at
		FROM api_tokens t JOIN users u ON u.id=t.user_id
		WHERE t.token_hash=?`, tokenHash).
		Scan(&u.ID, &u.Name, &admin, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.User{}, ErrNotFound
	}
	if err != nil {
		return api.User{}, err
	}
	u.IsAdmin = admin != 0
	// Best effort: a failed timestamp update must not fail the authentication it
	// is only annotating.
	s.db.Exec(`UPDATE api_tokens SET last_used=? WHERE token_hash=?`, time.Now().Unix(), tokenHash)
	return u, nil
}

// ListAPITokens returns a user's tokens, newest first. The secret is never
// stored in plain and so is never returned; the row is only metadata.
func (s *Store) ListAPITokens(userID string) ([]api.APIToken, error) {
	rows, err := s.db.Query(`SELECT id,name,created_at,last_used FROM api_tokens
		WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.APIToken{}
	for rows.Next() {
		var t api.APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteUserAPITokens revokes every API token a user holds and returns how many
// were cut. This rides along with "sign out other devices": an API token is a
// headless session, so cutting the user's other devices has to cut the agents
// holding a token too, or a leaked token outlives the passkey rotation meant to
// contain it.
func (s *Store) DeleteUserAPITokens(userID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE user_id=?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteAPIToken revokes a token. Scoped to the owning user so one user cannot
// revoke another's token by guessing its id.
func (s *Store) DeleteAPIToken(userID, id string) error {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Comics ---

// visibleComics returns a SQL boolean fragment, plus its args, that is true for
// exactly the comics userID may see: anything from the watched library root
// (server-wide by definition), anything they uploaded themselves, and anything
// sitting in a collection its owner has shared.
//
// It is a fragment rather than a view so it can be ANDed into any query. Every
// comic read path in this package must include it: enforcing visibility here
// rather than in handlers means a new handler cannot forget to.
//
// The first arm tests source='library' rather than a NULL owner_id, which is
// what makes claiming work: a claimed comic is still under the library root but
// its source is 'claimed', so it falls through to the owner arm and only its
// claimer sees it. The two must not be conflated here.
//
// The shared arm is restricted to collections owned by the comic's own owner
// because only the uploader may opt their upload in. Without that, anyone who
// could see a shared upload could add it to a collection of their own and share
// that, and the exposure would outlive the uploader's unshare — sharing would be
// irrevocable by anyone but the launderer. IS NOT DISTINCT FROM rather than = so
// the two NULL owner_ids of a library comic in a library owner's collection
// still match; library comics are covered by the source arm regardless.
func visibleComics(userID string) (string, []any) {
	const frag = `(comics.source='library'
		OR comics.owner_id=?
		OR EXISTS (
			SELECT 1 FROM collection_comics cc
			JOIN collections co ON co.id=cc.collection_id
			WHERE cc.comic_id=comics.id AND co.shared=1
				AND co.owner_id IS NOT DISTINCT FROM comics.owner_id))`
	return frag, []any{userID}
}

// ComicRow is a comic as written by the scanner and the importer. It carries the
// ownership fields that api.Comic deliberately does not expose to the client.
type ComicRow struct {
	ID          string
	Path        string
	ContentHash string
	Title       string
	Series      string
	Number      string
	Volume      string
	Summary     string
	PageCount   int
	FileSize    int64
	AddedAt     int64
	ModifiedAt  int64
	Missing     bool
	OwnerID     string // "" for library comics, which belong to the server
	Source      string
}

// owner_id and source ride along because the client needs to know whether a
// comic is claimable and whether the claim is the caller's. scanComics folds
// them into api.Comic.Source and api.Comic.OwnedByMe rather than exposing the
// owner's id, which is nobody's business but the server's.
const comicCols = `comics.id,comics.path,comics.title,comics.series,comics.number,comics.volume,
	comics.summary,comics.page_count,comics.file_size,comics.added_at,comics.modified_at,comics.missing,
	comics.owner_id,comics.source`

// UpsertComic inserts a comic or updates the existing row with the same path,
// keeping its id so tags and progress stay attached across a rescan.
func (s *Store) UpsertComic(c ComicRow) error {
	var owner any
	if c.OwnerID != "" {
		owner = c.OwnerID
	}
	if c.Source == "" {
		c.Source = SourceLibrary
	}
	if c.AddedAt == 0 {
		c.AddedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO comics(id,path,content_hash,title,series,number,volume,summary,
		page_count,file_size,added_at,modified_at,missing,owner_id,source)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
			content_hash=excluded.content_hash, title=excluded.title, series=excluded.series,
			number=excluded.number, volume=excluded.volume, summary=excluded.summary,
			page_count=excluded.page_count, file_size=excluded.file_size,
			modified_at=excluded.modified_at, missing=excluded.missing`,
		c.ID, c.Path, c.ContentHash, c.Title, c.Series, c.Number, c.Volume, c.Summary,
		c.PageCount, c.FileSize, c.AddedAt, c.ModifiedAt, boolInt(c.Missing), owner, c.Source)
	return err
}

// ComicRowByPath returns the raw row for a path, ignoring visibility. Scanner
// and importer only — it is not reachable from a request.
func (s *Store) ComicRowByPath(path string) (ComicRow, error) {
	return s.comicRowWhere(`comics.path=?`, path)
}

// ComicRowByHash returns the raw row with a matching content hash, ignoring
// visibility. This is the scanner's rename fallback when the path misses.
func (s *Store) ComicRowByHash(hash string) (ComicRow, error) {
	if hash == "" {
		return ComicRow{}, ErrNotFound
	}
	return s.comicRowWhere(`comics.content_hash=?`, hash)
}

// ComicRowByID returns the raw row for an id, ignoring visibility. Handlers must
// gate on GetComic first and use this only to reach the fields api.Comic does not
// carry — the on-disk source and owner.
func (s *Store) ComicRowByID(id string) (ComicRow, error) {
	return s.comicRowWhere(`comics.id=?`, id)
}

// CountLibraryComics counts the comics under the watched root whose file is
// present. Missing rows are excluded: they are kept so that a remount restores
// them, but a status card that counts comics nobody can open is lying. Claimed
// comics count too — this is how many files the scanner is responsible for, not
// how many any one user can see, which is CountVisibleComics.
func (s *Store) CountLibraryComics() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM comics WHERE source IN (?,?) AND missing=0`,
		SourceLibrary, SourceClaimed).Scan(&n)
	return n, err
}

func (s *Store) comicRowWhere(where string, args ...any) (ComicRow, error) {
	var c ComicRow
	var owner sql.NullString
	var missing int
	err := s.db.QueryRow(`SELECT id,path,content_hash,title,series,number,volume,summary,
		page_count,file_size,added_at,modified_at,missing,owner_id,source
		FROM comics WHERE `+where, args...).
		Scan(&c.ID, &c.Path, &c.ContentHash, &c.Title, &c.Series, &c.Number, &c.Volume,
			&c.Summary, &c.PageCount, &c.FileSize, &c.AddedAt, &c.ModifiedAt, &missing, &owner, &c.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.Missing = missing != 0
	c.OwnerID = owner.String
	return c, err
}

// MovedComic repoints an existing row at a new path, which is how a rename keeps
// its tags and progress instead of appearing as a delete plus an add.
func (s *Store) MovedComic(id, newPath string, modifiedAt int64) error {
	_, err := s.db.Exec(`UPDATE comics SET path=?, missing=0, modified_at=? WHERE id=?`, newPath, modifiedAt, id)
	return err
}

// SetComicMissing flags or clears the missing marker. Rows are kept rather than
// deleted so an unmounted volume cannot silently destroy tags and progress.
func (s *Store) SetComicMissing(id string, missing bool) error {
	_, err := s.db.Exec(`UPDATE comics SET missing=? WHERE id=?`, boolInt(missing), id)
	return err
}

// ListComicPaths returns every known path under the library root, for the
// scanner to diff the filesystem against. Claimed comics are included: claiming
// does not move the file, so a claimed row whose file is gone is exactly as
// missing as a library one, and leaving it out would mean a deleted claim never
// gets flagged.
func (s *Store) ListComicPaths() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT id,path FROM comics WHERE source IN (?,?)`,
		SourceLibrary, SourceClaimed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[p] = id
	}
	return out, rows.Err()
}

// ClaimComic takes a library comic into userID's own library: the row gains an
// owner and stops being server-wide, so everyone else loses sight of it. The
// file does not move — only a library comic can be claimed, and its path stays
// relative to the library root.
//
// Restricting the source to 'library' is what keeps this from being a way to
// take somebody else's upload: an upload already has an owner, and claiming is
// only defined for the comics that have none.
func (s *Store) ClaimComic(userID, comicID string) error {
	res, err := s.db.Exec(`UPDATE comics SET owner_id=?, source=?
		WHERE id=? AND source=?`, userID, SourceClaimed, comicID, SourceLibrary)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UnclaimComic hands a claimed comic back to the server, making it server-wide
// again. Only the claimer may, unless the caller is an admin: a claim is
// personal, but an admin has to be able to undo one made by an account that is
// no longer around to undo it themselves.
func (s *Store) UnclaimComic(userID string, isAdmin bool, comicID string) error {
	q := `UPDATE comics SET owner_id=NULL, source=? WHERE id=? AND source=?`
	args := []any{SourceLibrary, comicID, SourceClaimed}
	if !isAdmin {
		q += ` AND owner_id=?`
		args = append(args, userID)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteComic removes a comic row outright.
func (s *Store) DeleteComic(id string) error {
	res, err := s.db.Exec(`DELETE FROM comics WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetComic returns one comic if userID may see it, else ErrNotFound.
func (s *Store) GetComic(userID, id string) (api.Comic, error) {
	vis, args := visibleComics(userID)
	all := append([]any{}, args...)
	all = append(all, id)
	rows, err := s.db.Query(`SELECT `+comicCols+` FROM comics WHERE `+vis+` AND comics.id=?`, all...)
	if err != nil {
		return api.Comic{}, err
	}
	out, err := s.scanComics(userID, rows)
	if err != nil {
		return api.Comic{}, err
	}
	if len(out) == 0 {
		return api.Comic{}, ErrNotFound
	}
	return out[0], nil
}

// ListComics returns every comic userID may see, newest first.
func (s *Store) ListComics(userID string) ([]api.Comic, error) {
	vis, args := visibleComics(userID)
	rows, err := s.db.Query(`SELECT `+comicCols+` FROM comics WHERE `+vis+`
		ORDER BY comics.series, comics.number, comics.added_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	return s.scanComics(userID, rows)
}

// ListComicsInCollection returns a collection's comics in their stored order,
// still filtered by what userID may see.
func (s *Store) ListComicsInCollection(userID, collectionID string) ([]api.Comic, error) {
	vis, args := visibleComics(userID)
	all := append([]any{}, args...)
	all = append(all, collectionID)
	rows, err := s.db.Query(`SELECT `+comicCols+` FROM comics
		JOIN collection_comics cc ON cc.comic_id=comics.id
		WHERE `+vis+` AND cc.collection_id=? ORDER BY cc.position`, all...)
	if err != nil {
		return nil, err
	}
	return s.scanComics(userID, rows)
}

// ComicFilter narrows a library listing. A zero field is not a filter; Limit 0
// returns everything from Offset on.
type ComicFilter struct {
	Tag    string
	Series string
	// Query matches a substring of the title or the series.
	Query      string
	Collection string
	Offset     int
	Limit      int
}

// ListComicsFiltered returns one page of the comics userID may see under a
// filter, plus the total matching it before pagination.
//
// Filtering happens in SQL alongside the visibility fragment rather than in the
// handler, for the same reason visibility does: a listing filtered in Go would
// have to load every comic the user can see to return twenty of them.
func (s *Store) ListComicsFiltered(userID string, f ComicFilter) ([]api.Comic, int, error) {
	vis, args := visibleComics(userID)
	from := `FROM comics`
	where := []string{vis}
	order := ` ORDER BY comics.series, comics.number, comics.added_at DESC`
	if f.Collection != "" {
		from += ` JOIN collection_comics cc ON cc.comic_id=comics.id
			JOIN collections ON collections.id=cc.collection_id`
		// Defence in depth: the visibility fragment already decides which comics
		// come back, and a filter must never be able to widen that. Gating the
		// collection the caller filters by on the same rule the collection reads
		// use keeps this filter from being the one place the premise breaks.
		where = append(where, `cc.collection_id=?`, visibleCollections)
		args = append(args, f.Collection, userID)
		// A collection's order is the point of a collection, so it wins over the
		// library's series ordering whenever one is being listed.
		order = ` ORDER BY cc.position`
	}
	if f.Tag != "" {
		// Matched against the caller's own tag rows: filtering by a name another
		// user coined must find nothing rather than their comics.
		where = append(where, `EXISTS (SELECT 1 FROM comic_tags ct JOIN tags t ON t.id=ct.tag_id
			WHERE ct.comic_id=comics.id AND t.user_id=? AND t.name=?)`)
		args = append(args, userID, f.Tag)
	}
	if f.Series != "" {
		where = append(where, `comics.series=?`)
		args = append(args, f.Series)
	}
	if f.Query != "" {
		// LIKE is case-insensitive over ASCII in SQLite by default, which is the
		// behaviour a search box wants.
		where = append(where, `(comics.title LIKE ? ESCAPE '\' OR comics.series LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(f.Query) + "%"
		args = append(args, like, like)
	}
	cond := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) `+from+` WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT ` + comicCols + ` ` + from + ` WHERE ` + cond + order
	if f.Limit > 0 {
		q += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	out, err := s.scanComics(userID, rows)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// escapeLike neutralises the wildcards in a user's search string so a query of
// "%" means the literal character rather than "every comic".
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// CountVisibleComics counts what userID may see, for the library status card.
func (s *Store) CountVisibleComics(userID string) (int, error) {
	vis, args := visibleComics(userID)
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM comics WHERE `+vis, args...).Scan(&n)
	return n, err
}

// scanComics materialises comic rows and attaches userID's tags. It closes rows.
// userID is needed for the tags: they are per-user, so the same row lists
// different labels depending on who is reading it.
func (s *Store) scanComics(userID string, rows *sql.Rows) ([]api.Comic, error) {
	defer rows.Close()
	var out []api.Comic
	var ids []string
	for rows.Next() {
		var c api.Comic
		var missing int
		var owner sql.NullString
		if err := rows.Scan(&c.ID, &c.Path, &c.Title, &c.Series, &c.Number, &c.Volume,
			&c.Summary, &c.PageCount, &c.FileSize, &c.AddedAt, &c.ModifiedAt, &missing,
			&owner, &c.Source); err != nil {
			return nil, err
		}
		c.Missing = missing != 0
		c.OwnedByMe = owner.Valid && owner.String == userID
		c.Tags = []string{}
		out = append(out, c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	tags, err := s.tagsForComics(userID, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if t := tags[out[i].ID]; t != nil {
			out[i].Tags = t
		}
	}
	return out, nil
}

// --- Tags ---

// tagsForComics loads tags for a set of comics in one query rather than one per
// comic, which is the difference between a library list being instant and being
// a few hundred round trips.
func (s *Store) tagsForComics(userID string, ids []string) (map[string][]string, error) {
	q := `SELECT ct.comic_id, t.name FROM comic_tags ct JOIN tags t ON t.id=ct.tag_id
		WHERE t.user_id=? AND ct.comic_id IN (` + placeholders(len(ids)) + `) ORDER BY t.name`
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var comicID, name string
		if err := rows.Scan(&comicID, &name); err != nil {
			return nil, err
		}
		out[comicID] = append(out[comicID], name)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// SetComicTags replaces userID's tags on a comic, creating tag rows as needed.
// It refuses a comic the user cannot see, and nothing else: a tag is the
// caller's own label on someone else's shelf, so it is writable by anyone who
// can read the comic. Ownership does not enter into it — writing a tag cannot
// affect what any other user reads, because no other user can see it.
func (s *Store) SetComicTags(userID, comicID string, tags []string) error {
	if _, err := s.GetComic(userID, comicID); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Scoped to this user's tag rows: a full delete would strip everyone else's
	// tags off the comic on every save.
	if _, err := tx.Exec(`DELETE FROM comic_tags WHERE comic_id=?
		AND tag_id IN (SELECT id FROM tags WHERE user_id=?)`, comicID, userID); err != nil {
		return err
	}
	for _, name := range tags {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO tags(id,user_id,name) VALUES(?,?,?)
			ON CONFLICT(user_id,name) DO NOTHING`, randID(), userID, name); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO comic_tags(comic_id,tag_id)
			SELECT ?, id FROM tags WHERE user_id=? AND name=? ON CONFLICT DO NOTHING`,
			comicID, userID, name); err != nil {
			return err
		}
	}
	// Tags nobody references are noise in the sidebar, so drop them here rather
	// than on a sweep that may never run. Scoped to this user: another user's
	// orphans are not this transaction's business.
	if _, err := tx.Exec(`DELETE FROM tags WHERE user_id=?
		AND id NOT IN (SELECT tag_id FROM comic_tags)`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListTags returns userID's own tags with the count of comics they may see under
// each. Tags are per-user, so this never shows another user's vocabulary; the
// visibility fragment still applies because a comic can stop being visible after
// it was tagged, and a count must not advertise one that has.
func (s *Store) ListTags(userID string) ([]api.Tag, error) {
	vis, args := visibleComics(userID)
	args = append(args, userID)
	rows, err := s.db.Query(`SELECT t.name, COUNT(*) FROM tags t
		JOIN comic_tags ct ON ct.tag_id=t.id
		JOIN comics ON comics.id=ct.comic_id
		WHERE `+vis+` AND t.user_id=?
		GROUP BY t.id ORDER BY t.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Tag{}
	for rows.Next() {
		var t api.Tag
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- Collections ---

// CreateCollection inserts a collection owned by userID.
func (s *Store) CreateCollection(id, userID, name, summary string, shared bool) (api.Collection, error) {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO collections(id,name,summary,owner_id,shared,created_at)
		VALUES(?,?,?,?,?,?)`, id, name, summary, userID, boolInt(shared), now)
	if err != nil {
		return api.Collection{}, err
	}
	return api.Collection{ID: id, Name: name, Summary: summary, OwnerID: userID, Shared: shared, CreatedAt: now}, nil
}

// visibleCollections is the collection-level counterpart of visibleComics: your
// own, or somebody else's that they shared.
const visibleCollections = `(collections.owner_id=? OR collections.shared=1)`

const collectionCols = `collections.id,collections.name,collections.summary,collections.owner_id,
	users.name,collections.shared,collections.created_at,collections.cover_comic_id,
	(SELECT COUNT(*) FROM collection_comics cc WHERE cc.collection_id=collections.id)`

// GetCollection returns a collection userID may see.
func (s *Store) GetCollection(userID, id string) (api.Collection, error) {
	rows, err := s.db.Query(`SELECT `+collectionCols+` FROM collections
		JOIN users ON users.id=collections.owner_id
		WHERE `+visibleCollections+` AND collections.id=?`, userID, id)
	if err != nil {
		return api.Collection{}, err
	}
	out, err := scanCollections(rows)
	if err != nil {
		return api.Collection{}, err
	}
	if len(out) == 0 {
		return api.Collection{}, ErrNotFound
	}
	return out[0], nil
}

// ListCollections returns collections userID may see.
func (s *Store) ListCollections(userID string) ([]api.Collection, error) {
	rows, err := s.db.Query(`SELECT `+collectionCols+` FROM collections
		JOIN users ON users.id=collections.owner_id
		WHERE `+visibleCollections+` ORDER BY collections.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return scanCollections(rows)
}

func scanCollections(rows *sql.Rows) ([]api.Collection, error) {
	defer rows.Close()
	out := []api.Collection{}
	for rows.Next() {
		var c api.Collection
		var shared int
		var cover sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Summary, &c.OwnerID, &c.OwnerName,
			&shared, &c.CreatedAt, &cover, &c.Count); err != nil {
			return nil, err
		}
		c.Shared = shared != 0
		c.CoverComicID = cover.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// OwnedCollection returns a collection only if userID owns it. Shared makes a
// collection readable, never writable, so every mutation goes through this.
func (s *Store) OwnedCollection(userID, id string) (api.Collection, error) {
	c, err := s.GetCollection(userID, id)
	if err != nil {
		return c, err
	}
	if c.OwnerID != userID {
		return api.Collection{}, ErrNotFound
	}
	return c, nil
}

// UpdateCollection applies a partial update to a collection the user owns.
func (s *Store) UpdateCollection(userID, id string, req api.UpdateCollectionRequest) error {
	if _, err := s.OwnedCollection(userID, id); err != nil {
		return err
	}
	var sets []string
	var args []any
	if req.Name != nil {
		sets = append(sets, "name=?")
		args = append(args, *req.Name)
	}
	if req.Summary != nil {
		sets = append(sets, "summary=?")
		args = append(args, *req.Summary)
	}
	if req.Shared != nil {
		sets = append(sets, "shared=?")
		args = append(args, boolInt(*req.Shared))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := s.db.Exec(`UPDATE collections SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

// SetCollectionCover points a collection at a cover comic.
func (s *Store) SetCollectionCover(userID, id, comicID string) error {
	if _, err := s.OwnedCollection(userID, id); err != nil {
		return err
	}
	if _, err := s.GetComic(userID, comicID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE collections SET cover_comic_id=? WHERE id=?`, comicID, id)
	return err
}

// DeleteCollection removes a collection the user owns. Membership cascades; the
// comics themselves are untouched.
func (s *Store) DeleteCollection(userID, id string) error {
	if _, err := s.OwnedCollection(userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM collections WHERE id=?`, id)
	return err
}

// AddToCollection appends a comic to the end of a collection the user owns.
func (s *Store) AddToCollection(userID, collectionID, comicID string) error {
	if _, err := s.OwnedCollection(userID, collectionID); err != nil {
		return err
	}
	if _, err := s.GetComic(userID, comicID); err != nil {
		return err
	}
	var next int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(position),-1)+1 FROM collection_comics WHERE collection_id=?`,
		collectionID).Scan(&next); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO collection_comics(collection_id,comic_id,position)
		VALUES(?,?,?) ON CONFLICT DO NOTHING`, collectionID, comicID, next)
	return err
}

// RemoveFromCollection drops a comic from a collection the user owns.
func (s *Store) RemoveFromCollection(userID, collectionID, comicID string) error {
	if _, err := s.OwnedCollection(userID, collectionID); err != nil {
		return err
	}
	res, err := s.db.Exec(`DELETE FROM collection_comics WHERE collection_id=? AND comic_id=?`,
		collectionID, comicID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReorderCollection rewrites the whole order from the given comic ids. Taking
// the full list rather than a move instruction keeps the stored positions dense
// and makes the operation idempotent.
func (s *Store) ReorderCollection(userID, collectionID string, comicIDs []string) error {
	if _, err := s.OwnedCollection(userID, collectionID); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range comicIDs {
		if _, err := tx.Exec(`UPDATE collection_comics SET position=? WHERE collection_id=? AND comic_id=?`,
			i, collectionID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- Progress ---

// GetProgress returns the caller's position in a comic they may see.
func (s *Store) GetProgress(userID, comicID string) (api.Progress, error) {
	c, err := s.GetComic(userID, comicID)
	if err != nil {
		return api.Progress{}, err
	}
	var p api.Progress
	var completed int
	err = s.db.QueryRow(`SELECT comic_id,page,completed,updated_at FROM progress
		WHERE user_id=? AND comic_id=?`, userID, comicID).
		Scan(&p.ComicID, &p.Page, &completed, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.Progress{}, ErrNotFound
	}
	p.Completed = completed != 0
	p.PageCount = c.PageCount
	return p, err
}

// SetProgress records the caller's position in a comic they may see, stamped
// with the server clock.
func (s *Store) SetProgress(userID, comicID string, page int, completed bool) (api.Progress, error) {
	return s.SetProgressAt(userID, comicID, page, completed, time.Now().Unix())
}

// SetProgressAt is SetProgress with the observation time supplied by the caller.
// The row's updated_at is what later writes are ordered against, so a replayed
// offline write has to store the moment it was read at rather than the moment it
// arrived — otherwise every replay would look like the newest position on the
// server. Callers are responsible for bounding updatedAt; see handleSetProgress.
func (s *Store) SetProgressAt(userID, comicID string, page int, completed bool, updatedAt int64) (api.Progress, error) {
	c, err := s.GetComic(userID, comicID)
	if err != nil {
		return api.Progress{}, err
	}
	_, err = s.db.Exec(`INSERT INTO progress(user_id,comic_id,page,completed,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(user_id,comic_id) DO UPDATE SET
			page=excluded.page, completed=excluded.completed, updated_at=excluded.updated_at`,
		userID, comicID, page, boolInt(completed), updatedAt)
	if err != nil {
		return api.Progress{}, err
	}
	return api.Progress{ComicID: comicID, Page: page, PageCount: c.PageCount, Completed: completed, UpdatedAt: updatedAt}, nil
}

// ListProgress returns the caller's progress across every comic they may see.
func (s *Store) ListProgress(userID string) ([]api.Progress, error) {
	vis, args := visibleComics(userID)
	all := []any{userID}
	all = append(all, args...)
	rows, err := s.db.Query(`SELECT p.comic_id,p.page,p.completed,p.updated_at,comics.page_count
		FROM progress p JOIN comics ON comics.id=p.comic_id
		WHERE p.user_id=? AND `+vis+` ORDER BY p.updated_at DESC`, all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Progress{}
	for rows.Next() {
		var p api.Progress
		var completed int
		if err := rows.Scan(&p.ComicID, &p.Page, &completed, &p.UpdatedAt, &p.PageCount); err != nil {
			return nil, err
		}
		p.Completed = completed != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Import jobs ---

const jobCols = `id,name,owner_id,stage,done,total,source_count,page_count,exact_dupes,near_dupes,
	message,comic_id,started_at,finished_at`

// SaveImportJob upserts a job row.
func (s *Store) SaveImportJob(j api.ImportJob) error {
	var comicID any
	if j.ComicID != "" {
		comicID = j.ComicID
	}
	_, err := s.db.Exec(`INSERT INTO import_jobs(`+jobCols+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, stage=excluded.stage, done=excluded.done, total=excluded.total,
			source_count=excluded.source_count, page_count=excluded.page_count,
			exact_dupes=excluded.exact_dupes, near_dupes=excluded.near_dupes,
			message=excluded.message, comic_id=excluded.comic_id, finished_at=excluded.finished_at`,
		j.ID, j.Name, j.OwnerID, string(j.Stage), j.Done, j.Total, j.SourceCount, j.PageCount,
		j.ExactDupes, j.NearDupes, j.Message, comicID, j.StartedAt, j.FinishedAt)
	return err
}

// GetImportJob returns a job owned by userID.
func (s *Store) GetImportJob(userID, id string) (api.ImportJob, error) {
	rows, err := s.db.Query(`SELECT `+jobCols+` FROM import_jobs WHERE id=? AND owner_id=?`, id, userID)
	if err != nil {
		return api.ImportJob{}, err
	}
	out, err := scanJobs(rows)
	if err != nil {
		return api.ImportJob{}, err
	}
	if len(out) == 0 {
		return api.ImportJob{}, ErrNotFound
	}
	return out[0], nil
}

// ListImportJobs returns a user's jobs, newest first. Imports are never shared:
// a job is scoped to the user who started it regardless of where the comic ends
// up.
func (s *Store) ListImportJobs(userID string) ([]api.ImportJob, error) {
	rows, err := s.db.Query(`SELECT `+jobCols+` FROM import_jobs WHERE owner_id=? ORDER BY started_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// ListUnfinishedImportJobs returns jobs left in flight, for startup to mark as
// failed: the process that was running them is gone and nothing else will.
func (s *Store) ListUnfinishedImportJobs() ([]api.ImportJob, error) {
	rows, err := s.db.Query(`SELECT ` + jobCols + ` FROM import_jobs WHERE finished_at=0`)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

func scanJobs(rows *sql.Rows) ([]api.ImportJob, error) {
	defer rows.Close()
	out := []api.ImportJob{}
	for rows.Next() {
		var j api.ImportJob
		var stage string
		var comicID sql.NullString
		if err := rows.Scan(&j.ID, &j.Name, &j.OwnerID, &stage, &j.Done, &j.Total,
			&j.SourceCount, &j.PageCount, &j.ExactDupes, &j.NearDupes, &j.Message,
			&comicID, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		j.Stage = api.ImportStage(stage)
		j.ComicID = comicID.String
		out = append(out, j)
	}
	return out, rows.Err()
}
