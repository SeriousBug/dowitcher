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
	// SourceLibraryPDF is a comic converted from a PDF dropped into the watched
	// library folder. It is server-wide like a library comic, but the scanner does
	// not manage it: its CBZ lives in the uploads (data) dir, not under the library
	// root, because the library root is read-only by contract and the read-only
	// mount is exactly what this source exists to tolerate. The source PDF is left
	// where it was — its folder is never written to — so a library-pdf comic is
	// deduped by content hash rather than by the source file being removed.
	SourceLibraryPDF = "library-pdf"
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

// --- OAuth ---

// OAuthClient is a dynamically-registered MCP client. RedirectURIs is the exact
// set an authorize request is matched against — no prefix or wildcard, so a
// mismatch is an open-redirect attempt, not a near miss.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    int64
}

// AuthorizationCode is a redeemed code's row, returned by ConsumeAuthorizationCode
// so the token endpoint can re-check every binding the code carries.
type AuthorizationCode struct {
	ClientID      string
	UserID        string
	RedirectURI   string
	CodeChallenge string
	Scope         string
	ExpiresAt     int64
}

// RefreshToken is a rotated refresh token's row, returned by ConsumeRefreshToken.
type RefreshToken struct {
	ClientID  string
	UserID    string
	Scope     string
	ExpiresAt int64
}

// redirect_uris is newline-joined; a redirect URI cannot contain a newline, so
// this round-trips losslessly without a join table.
const redirectSep = "\n"

// CreateOAuthClient stores a dynamically-registered client. The id is a random
// token minted by the caller and handed back as the client_id.
func (s *Store) CreateOAuthClient(id, name string, redirectURIs []string) error {
	_, err := s.db.Exec(`INSERT INTO oauth_clients(id,name,redirect_uris,created_at)
		VALUES(?,?,?,?)`, id, name, strings.Join(redirectURIs, redirectSep), time.Now().Unix())
	return err
}

// OAuthClient looks up a registered client by id, or ErrNotFound.
func (s *Store) OAuthClient(id string) (OAuthClient, error) {
	var c OAuthClient
	var uris string
	err := s.db.QueryRow(`SELECT id,name,redirect_uris,created_at FROM oauth_clients WHERE id=?`, id).
		Scan(&c.ID, &c.Name, &uris, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthClient{}, ErrNotFound
	}
	if err != nil {
		return OAuthClient{}, err
	}
	if uris != "" {
		c.RedirectURIs = strings.Split(uris, redirectSep)
	}
	return c, nil
}

// CreateAuthorizationCode stores a hashed authorization code bound to a client,
// user, redirect URI and PKCE challenge. All four are re-checked when the code
// is redeemed.
func (s *Store) CreateAuthorizationCode(codeHash, clientID, userID, redirectURI, challenge, scope string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO oauth_authorization_codes
		(code_hash,client_id,user_id,redirect_uri,code_challenge,scope,expires_at,created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		codeHash, clientID, userID, redirectURI, challenge, scope, expiresAt, time.Now().Unix())
	return err
}

// ConsumeAuthorizationCode fetches and deletes a code in one transaction, so a
// replay of the same code finds nothing. An expired or absent code is
// ErrNotFound — the same reason ConsumeInvite fetch-and-deletes rather than
// relying on RETURNING, which this codebase does not use.
func (s *Store) ConsumeAuthorizationCode(codeHash string) (AuthorizationCode, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return AuthorizationCode{}, err
	}
	defer tx.Rollback()
	var c AuthorizationCode
	err = tx.QueryRow(`SELECT client_id,user_id,redirect_uri,code_challenge,scope,expires_at
		FROM oauth_authorization_codes WHERE code_hash=? AND expires_at>?`,
		codeHash, time.Now().Unix()).
		Scan(&c.ClientID, &c.UserID, &c.RedirectURI, &c.CodeChallenge, &c.Scope, &c.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthorizationCode{}, ErrNotFound
	}
	if err != nil {
		return AuthorizationCode{}, err
	}
	// Delete regardless of whether the row was expired-but-present: the SELECT's
	// expiry filter means an expired code already returned ErrNotFound above, so
	// reaching here means a live code we are now spending.
	if _, err := tx.Exec(`DELETE FROM oauth_authorization_codes WHERE code_hash=?`, codeHash); err != nil {
		return AuthorizationCode{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuthorizationCode{}, err
	}
	return c, nil
}

// CreateAccessToken stores a hashed access token.
func (s *Store) CreateAccessToken(tokenHash, clientID, userID, scope string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO oauth_access_tokens(token_hash,client_id,user_id,scope,expires_at,created_at)
		VALUES(?,?,?,?,?,?)`, tokenHash, clientID, userID, scope, expiresAt, time.Now().Unix())
	return err
}

// AccessTokenUser resolves an access token hash to its user, with expiry in the
// WHERE clause rather than a check on the result — the same shape as SessionUser,
// so there is no window in which a caller forgets to apply it. The real expiry
// is returned so the MCP bearer middleware can report it truthfully.
func (s *Store) AccessTokenUser(tokenHash string) (u api.User, expiresAt int64, err error) {
	var admin int
	err = s.db.QueryRow(`SELECT u.id,u.name,u.is_admin,u.created_at,t.expires_at
		FROM oauth_access_tokens t JOIN users u ON u.id=t.user_id
		WHERE t.token_hash=? AND t.expires_at>?`, tokenHash, time.Now().Unix()).
		Scan(&u.ID, &u.Name, &admin, &u.CreatedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return api.User{}, 0, ErrNotFound
	}
	if err != nil {
		return api.User{}, 0, err
	}
	u.IsAdmin = admin != 0
	return u, expiresAt, nil
}

// CreateRefreshToken stores a hashed refresh token.
func (s *Store) CreateRefreshToken(tokenHash, clientID, userID, scope string, expiresAt int64) error {
	_, err := s.db.Exec(`INSERT INTO oauth_refresh_tokens(token_hash,client_id,user_id,scope,expires_at,created_at)
		VALUES(?,?,?,?,?,?)`, tokenHash, clientID, userID, scope, expiresAt, time.Now().Unix())
	return err
}

// ConsumeRefreshToken fetches and deletes a refresh token in one transaction:
// every use rotates the token, so the old value is spent the moment it is read
// and a replayed refresh finds nothing. Expired or absent is ErrNotFound.
func (s *Store) ConsumeRefreshToken(tokenHash string) (RefreshToken, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return RefreshToken{}, err
	}
	defer tx.Rollback()
	var t RefreshToken
	err = tx.QueryRow(`SELECT client_id,user_id,scope,expires_at
		FROM oauth_refresh_tokens WHERE token_hash=? AND expires_at>?`,
		tokenHash, time.Now().Unix()).
		Scan(&t.ClientID, &t.UserID, &t.Scope, &t.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshToken{}, ErrNotFound
	}
	if err != nil {
		return RefreshToken{}, err
	}
	if _, err := tx.Exec(`DELETE FROM oauth_refresh_tokens WHERE token_hash=?`, tokenHash); err != nil {
		return RefreshToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return RefreshToken{}, err
	}
	return t, nil
}

// DeleteUserOAuthTokens revokes every access and refresh token a user holds and
// returns how many were cut. This rides along with "sign out other devices": an
// OAuth grant is a headless session, so cutting the user's other devices has to
// cut the agents holding a token too, or a leaked token outlives the passkey
// rotation meant to contain it. Registered clients and unredeemed codes are
// left — a client row is not a credential, and a code expires in a minute.
func (s *Store) DeleteUserOAuthTokens(userID string) (int64, error) {
	access, err := s.db.Exec(`DELETE FROM oauth_access_tokens WHERE user_id=?`, userID)
	if err != nil {
		return 0, err
	}
	refresh, err := s.db.Exec(`DELETE FROM oauth_refresh_tokens WHERE user_id=?`, userID)
	if err != nil {
		return 0, err
	}
	a, _ := access.RowsAffected()
	r, _ := refresh.RowsAffected()
	return a + r, nil
}

// --- Comics ---

// visibleComics returns a SQL boolean fragment, plus its args, that is true for
// exactly the comics userID may see: anything server-wide (a library comic, or a
// comic converted from a library-dropped PDF), anything they uploaded
// themselves, and anything sitting in a collection its owner has shared.
//
// It is a fragment rather than a view so it can be ANDed into any query. Every
// comic read path in this package must include it: enforcing visibility here
// rather than in handlers means a new handler cannot forget to.
//
// The first arm tests source rather than a NULL owner_id, which is what makes
// claiming work: a claimed comic is still under the library root but its source
// is 'claimed', so it falls through to the owner arm and only its claimer sees
// it. The two must not be conflated here. 'library-pdf' joins the server-wide arm
// because a PDF dropped into the shared folder is meant for everyone, exactly
// like the CBZ next to it — the only difference is where its file lives.
//
// The shared arm is restricted to collections owned by the comic's own owner
// because only the uploader may opt their upload in. Without that, anyone who
// could see a shared upload could add it to a collection of their own and share
// that, and the exposure would outlive the uploader's unshare — sharing would be
// irrevocable by anyone but the launderer. IS NOT DISTINCT FROM rather than = so
// the two NULL owner_ids of a library comic in a library owner's collection
// still match; library comics are covered by the source arm regardless.
func visibleComics(userID string) (string, []any) {
	const frag = `(comics.source IN ('library','library-pdf')
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
//
// The title column is the effective title: a user-set title_override wins over
// the scanned title, and an empty override falls through to it. This is a read
// path (api.Comic), not the scanner's — ComicRow keeps the raw scanned title so
// the scanner still diffs against what the file actually says.
const comicCols = `comics.id,comics.path,` + effectiveTitle + `,comics.series,comics.number,comics.volume,
	comics.summary,comics.page_count,comics.file_size,comics.added_at,comics.modified_at,comics.missing,
	comics.owner_id,comics.source`

// effectiveTitle is the display title expression: the override when set, else
// the scanned title. Named once so the listing filter and the column list agree.
const effectiveTitle = `COALESCE(NULLIF(comics.title_override,''),comics.title)`

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

// ServerWideComicByHash returns the raw server-wide row with a matching content
// hash, ignoring visibility. It is the library-pdf importer's dedupe: the source
// PDF is never deleted, so a restart re-converts it, and the conversion must not
// become a second copy of a comic everyone can already see. Scoped to the
// server-wide sources so it can never collide a fresh library-pdf with some
// user's private upload of the same bytes.
func (s *Store) ServerWideComicByHash(hash string) (ComicRow, error) {
	if hash == "" {
		return ComicRow{}, ErrNotFound
	}
	return s.comicRowWhere(`comics.content_hash=? AND comics.source IN (?,?)`,
		hash, SourceLibrary, SourceLibraryPDF)
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

// RenameComic sets a comic's title_override, which wins over the scanned title
// everywhere the effective title is read. It does not gate on ownership — the
// override is a server-wide field, so callers decide who may write it (an
// upload's owner, a claim's claimer, or an admin) the same way DeleteComic's
// caller does. Visibility is likewise the caller's to check first.
func (s *Store) RenameComic(id, title string) error {
	res, err := s.db.Exec(`UPDATE comics SET title_override=? WHERE id=?`, title, id)
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
		// behaviour a search box wants. Matched against the effective title so a
		// renamed comic is found by its new name, not the one on disk.
		where = append(where, `(`+effectiveTitle+` LIKE ? ESCAPE '\' OR comics.series LIKE ? ESCAPE '\')`)
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

// Collection kinds. A reading list is a collection whose kind is 'readinglist';
// they share every table and code path and differ only in which page lists them.
const (
	KindCollection  = "collection"
	KindReadingList = "readinglist"
)

// CreateCollection inserts a collection owned by userID. kind is normalised: an
// empty or unknown kind becomes a plain collection, so a caller cannot conjure a
// third kind the UI has no page for.
func (s *Store) CreateCollection(id, userID, name, summary, kind string, shared bool) (api.Collection, error) {
	kind = normalizeKind(kind)
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO collections(id,name,summary,owner_id,shared,created_at,kind)
		VALUES(?,?,?,?,?,?,?)`, id, name, summary, userID, boolInt(shared), now, kind)
	if err != nil {
		return api.Collection{}, err
	}
	return api.Collection{ID: id, Name: name, Summary: summary, OwnerID: userID, Shared: shared, CreatedAt: now, Kind: kind}, nil
}

// normalizeKind folds anything that is not a known kind down to KindCollection.
func normalizeKind(kind string) string {
	if kind == KindReadingList {
		return KindReadingList
	}
	return KindCollection
}

// visibleCollections is the collection-level counterpart of visibleComics: your
// own, or somebody else's that they shared.
const visibleCollections = `(collections.owner_id=? OR collections.shared=1)`

// cover_comic_id falls back to the first comic in the collection's order when
// the owner has not pinned one, so a non-empty collection always yields a cover.
// The pinned id is kept even if that comic later leaves the collection: it was
// a deliberate pick, and SetCollectionCover already checked it was visible.
const collectionCols = `collections.id,collections.name,collections.summary,collections.owner_id,
	users.name,collections.shared,collections.created_at,
	COALESCE(collections.cover_comic_id,
		(SELECT cc.comic_id FROM collection_comics cc
			WHERE cc.collection_id=collections.id ORDER BY cc.position LIMIT 1)),
	collections.kind,
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

// ListCollections returns collections userID may see. An empty kind returns
// every kind; a non-empty one (KindCollection or KindReadingList) is what lets
// the Collections and Reading lists pages each ask for only their own.
func (s *Store) ListCollections(userID, kind string) ([]api.Collection, error) {
	where := visibleCollections
	args := []any{userID}
	if kind != "" {
		where += ` AND collections.kind=?`
		args = append(args, normalizeKind(kind))
	}
	rows, err := s.db.Query(`SELECT `+collectionCols+` FROM collections
		JOIN users ON users.id=collections.owner_id
		WHERE `+where+` ORDER BY collections.created_at DESC`, args...)
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
			&shared, &c.CreatedAt, &cover, &c.Kind, &c.Count); err != nil {
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
	message,comic_id,started_at,finished_at,kind,queue_seq`

// SaveImportJob upserts a job row. input_path and options are left untouched:
// they are written once by SetImportJobInput and are not part of this shape, so
// the ON CONFLICT update cannot blank them when a running job saves a progress
// snapshot.
func (s *Store) SaveImportJob(j api.ImportJob) error {
	var comicID any
	if j.ComicID != "" {
		comicID = j.ComicID
	}
	// A library-pdf job has no owner: owner_id is nullable now, so an empty
	// OwnerID must land as NULL rather than a foreign key to a user id of "".
	var owner any
	if j.OwnerID != "" {
		owner = j.OwnerID
	}
	_, err := s.db.Exec(`INSERT INTO import_jobs(`+jobCols+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, stage=excluded.stage, done=excluded.done, total=excluded.total,
			source_count=excluded.source_count, page_count=excluded.page_count,
			exact_dupes=excluded.exact_dupes, near_dupes=excluded.near_dupes,
			message=excluded.message, comic_id=excluded.comic_id, finished_at=excluded.finished_at,
			kind=excluded.kind, queue_seq=excluded.queue_seq`,
		j.ID, j.Name, owner, string(j.Stage), j.Done, j.Total, j.SourceCount, j.PageCount,
		j.ExactDupes, j.NearDupes, j.Message, comicID, j.StartedAt, j.FinishedAt, j.Kind, j.QueueSeq)
	return err
}

// SetImportJobInput records the staged input and the options JSON a job runs
// with. These are server-only columns — they carry temp paths — so they are
// written through their own method rather than riding on the api.ImportJob shape
// the client sees.
func (s *Store) SetImportJobInput(id, inputPath, optionsJSON string) error {
	_, err := s.db.Exec(`UPDATE import_jobs SET input_path=?, options=? WHERE id=?`,
		inputPath, optionsJSON, id)
	return err
}

// RecoverableJob is an unfinished job plus the server-only fields a restart
// needs to re-enqueue it: where its input is staged and the options to run with.
type RecoverableJob struct {
	Job       api.ImportJob
	InputPath string
	Options   string
}

// ListRecoverableImportJobs returns every unfinished job with its staged input
// and options, for restart recovery to decide which can resume and which are
// lost. It replaces the read side of the old recover() sweep.
func (s *Store) ListRecoverableImportJobs() ([]RecoverableJob, error) {
	rows, err := s.db.Query(`SELECT ` + jobCols + `,input_path,options FROM import_jobs WHERE finished_at=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecoverableJob
	for rows.Next() {
		var r RecoverableJob
		if err := scanJobInto(rows, &r.Job, &r.InputPath, &r.Options); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllImportJobs returns every job across all owners, newest first, capped at
// limit. Admin-only snapshot: it includes ownerless library-pdf jobs, which no
// per-user query returns.
func (s *Store) ListAllImportJobs(limit int) ([]api.ImportJob, error) {
	rows, err := s.db.Query(`SELECT `+jobCols+` FROM import_jobs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	return scanJobs(rows)
}

// DeleteFinishedImportJobs removes a user's finished jobs. all clears every
// finished job regardless of owner, for an admin.
//
// A successful library-pdf job is exempt from both: it is not a mere audit line
// but the record that remembers a PDF still sitting in the read-only library
// folder was already converted. The source file is never deleted, so this row is
// the only thing standing between "already imported" and re-converting it on the
// next scan; clearing the visible import list must not take the library's memory
// with it. A failed library-pdf job carries no comic and is fair game to clear.
const keepImportedLibraryPDF = ` AND NOT (kind='library-pdf' AND comic_id IS NOT NULL)`

func (s *Store) DeleteFinishedImportJobs(ownerID string, all bool) error {
	if all {
		_, err := s.db.Exec(`DELETE FROM import_jobs WHERE finished_at<>0` + keepImportedLibraryPDF)
		return err
	}
	_, err := s.db.Exec(`DELETE FROM import_jobs WHERE finished_at<>0 AND owner_id=?`+keepImportedLibraryPDF, ownerID)
	return err
}

// HasUnfinishedImportJobForInput reports whether any unfinished job is already
// staged from inputPath. It is the manager's cross-restart dedup for a
// folder-dropped PDF: the recovery pass and the library scan can both reach the
// same file before either has populated the live map, so the DB is the shared
// ground truth that keeps it from being queued twice.
func (s *Store) HasUnfinishedImportJobForInput(inputPath string) (bool, error) {
	if inputPath == "" {
		return false, nil
	}
	var id string
	err := s.db.QueryRow(`SELECT id FROM import_jobs WHERE finished_at=0 AND input_path=? LIMIT 1`, inputPath).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// HasImportedInput reports whether a finished job already produced a comic from
// inputPath. It is the library-pdf importer's cross-restart skip: the source PDF
// stays on its read-only mount, so every restart re-hands it off, and re-running
// the conversion each time would be pure waste. A failed job (comic_id='') does
// not count — a PDF that failed last time is meant to be retried. These records
// are protected from the "clear finished imports" action (see
// keepImportedLibraryPDF), so this memory survives; the content-hash check at
// filing time is the last-resort backstop if a record is lost some other way.
func (s *Store) HasImportedInput(inputPath string) (bool, error) {
	if inputPath == "" {
		return false, nil
	}
	var id string
	err := s.db.QueryRow(`SELECT id FROM import_jobs
		WHERE input_path=? AND comic_id IS NOT NULL AND finished_at<>0 LIMIT 1`, inputPath).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MaxImportQueueSeq returns the highest queue_seq in use, so the manager can
// seed its monotonic counter past every row a restart recovers. 0 when empty.
func (s *Store) MaxImportQueueSeq() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(queue_seq),0) FROM import_jobs`).Scan(&n)
	return n, err
}

// QueuePaused reports whether the import queue is paused. The flag lives in the
// meta table because it is one server-wide boolean, not worth a migration.
func (s *Store) QueuePaused() (bool, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key='queue_paused'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// SetQueuePaused persists the queue's paused flag.
func (s *Store) SetQueuePaused(paused bool) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES('queue_paused',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, boolInt(paused))
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
		if err := scanJobInto(rows, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// scanJobInto scans one jobCols row into j. extra receives any trailing columns
// a caller selected past jobCols (input_path, options for recovery), in order.
func scanJobInto(rows *sql.Rows, j *api.ImportJob, extra ...any) error {
	var stage string
	var comicID, owner sql.NullString
	dst := []any{&j.ID, &j.Name, &owner, &stage, &j.Done, &j.Total,
		&j.SourceCount, &j.PageCount, &j.ExactDupes, &j.NearDupes, &j.Message,
		&comicID, &j.StartedAt, &j.FinishedAt, &j.Kind, &j.QueueSeq}
	dst = append(dst, extra...)
	if err := rows.Scan(dst...); err != nil {
		return err
	}
	j.Stage = api.ImportStage(stage)
	j.ComicID = comicID.String
	j.OwnerID = owner.String
	return nil
}

// ReorderImportJobs rewrites queue_seq densely from the given ordered id list,
// mirroring ReorderCollection: the full list keeps the stored positions dense
// and makes the operation idempotent. seqBase is the first sequence to assign,
// so a reorder can be pushed past whatever seq the running jobs already hold.
func (s *Store) ReorderImportJobs(ids []string, seqBase int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE import_jobs SET queue_seq=? WHERE id=?`, seqBase+int64(i), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
