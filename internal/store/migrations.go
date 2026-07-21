package store

import "strconv"

// migrations are applied in order and the slice index is the schema version.
// Append only — editing an existing entry silently diverges the schema of an
// already-migrated database from a fresh one.
var migrations = []string{
	`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);`,
	`CREATE TABLE credentials (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		cred_id BLOB NOT NULL UNIQUE,
		public_key BLOB NOT NULL,
		sign_count INTEGER NOT NULL DEFAULT 0,
		transports TEXT NOT NULL DEFAULT '',
		aaguid BLOB,
		name TEXT NOT NULL DEFAULT '',
		backup_eligible INTEGER NOT NULL DEFAULT 0,
		backup_state INTEGER NOT NULL DEFAULT 0,
		last_used INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);`,
	// for_user binds a recovery invite to an existing user: enrolling on it adds
	// a fresh passkey to that user rather than creating a new account.
	`CREATE TABLE invites (
		token TEXT PRIMARY KEY,
		created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
		for_user TEXT REFERENCES users(id) ON DELETE CASCADE,
		is_admin INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER NOT NULL,
		used_at INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);`,
	`CREATE TABLE sessions (
		token TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);`,
	// path is the stable identity of a comic across rescans, so it is unique and
	// relative to the library root: an absolute path would break the moment the
	// container's mount point changes. owner_id is NULL for comics found under
	// the library root, which belong to the server rather than to a user.
	`CREATE TABLE comics (
		id TEXT PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		content_hash TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		series TEXT NOT NULL DEFAULT '',
		number TEXT NOT NULL DEFAULT '',
		volume TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		page_count INTEGER NOT NULL DEFAULT 0,
		file_size INTEGER NOT NULL DEFAULT 0,
		added_at INTEGER NOT NULL,
		modified_at INTEGER NOT NULL DEFAULT 0,
		missing INTEGER NOT NULL DEFAULT 0,
		owner_id TEXT REFERENCES users(id) ON DELETE CASCADE,
		source TEXT NOT NULL DEFAULT 'library'
	);`,
	// content_hash is the rename fallback: a moved file keeps its tags and
	// progress because the scanner matches the hash when the path misses.
	`CREATE INDEX idx_comics_hash ON comics(content_hash);`,
	`CREATE TABLE tags (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE
	);`,
	`CREATE TABLE comic_tags (
		comic_id TEXT NOT NULL REFERENCES comics(id) ON DELETE CASCADE,
		tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
		PRIMARY KEY (comic_id, tag_id)
	);`,
	`CREATE INDEX idx_comic_tags_tag ON comic_tags(tag_id);`,
	`CREATE TABLE collections (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		shared INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		cover_comic_id TEXT REFERENCES comics(id) ON DELETE SET NULL
	);`,
	`CREATE TABLE collection_comics (
		collection_id TEXT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
		comic_id TEXT NOT NULL REFERENCES comics(id) ON DELETE CASCADE,
		position INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (collection_id, comic_id)
	);`,
	// Indexed on comic_id because the visibility check joins from a comic to the
	// collections holding it on every single comic read.
	`CREATE INDEX idx_collection_comics_comic ON collection_comics(comic_id);`,
	`CREATE TABLE progress (
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		comic_id TEXT NOT NULL REFERENCES comics(id) ON DELETE CASCADE,
		page INTEGER NOT NULL DEFAULT 0,
		completed INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (user_id, comic_id)
	);`,
	// Import jobs are persisted because a large import outlives the request that
	// started it, and a crash must leave a row saying so rather than a client
	// spinning forever on a job nobody remembers.
	`CREATE TABLE import_jobs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		stage TEXT NOT NULL DEFAULT '',
		done INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL DEFAULT 0,
		source_count INTEGER NOT NULL DEFAULT 0,
		page_count INTEGER NOT NULL DEFAULT 0,
		exact_dupes INTEGER NOT NULL DEFAULT 0,
		near_dupes INTEGER NOT NULL DEFAULT 0,
		message TEXT NOT NULL DEFAULT '',
		comic_id TEXT REFERENCES comics(id) ON DELETE SET NULL,
		started_at INTEGER NOT NULL,
		finished_at INTEGER NOT NULL DEFAULT 0
	);`,
	// Tags become per-user. The old tables were server-global — one `tags.name`
	// row shared by everyone — so there is no owner to migrate an existing row
	// to: a tag on a library comic could have been written by any user, and
	// attributing it to a guessed account would hand one person a vocabulary
	// they never wrote. The tables are dropped and everyone starts clean.
	`DROP TABLE comic_tags;`,
	`DROP TABLE tags;`,
	// name is unique per user rather than globally, so two users coining the
	// same word get two rows and neither can see the other's.
	`CREATE TABLE tags (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		UNIQUE (user_id, name)
	);`,
	// tag_id already names the owner via tags.user_id, so the join carries no
	// user column of its own.
	`CREATE TABLE comic_tags (
		comic_id TEXT NOT NULL REFERENCES comics(id) ON DELETE CASCADE,
		tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
		PRIMARY KEY (comic_id, tag_id)
	);`,
	`CREATE INDEX idx_comic_tags_tag ON comic_tags(tag_id);`,
	// API tokens authenticate a headless agent (the MCP server) as one user.
	// token_hash holds a SHA-256 of the secret, never the secret itself: a token
	// is long-lived and higher value than a session cookie, so a leaked database
	// must not hand over a live agent credential. The secret is high-entropy
	// random, so an unsalted fast hash is enough — there is nothing to brute
	// force. last_used records the most recent authentication for the UI.
	`CREATE TABLE api_tokens (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL DEFAULT '',
		token_hash TEXT NOT NULL UNIQUE,
		created_at INTEGER NOT NULL,
		last_used INTEGER NOT NULL DEFAULT 0
	);`,
	// The static API token is replaced by a full OAuth 2.1 authorization server:
	// Claude's connector UI and Claude Code only speak the MCP OAuth flow and
	// have no field for a pasted bearer, so a static token could never be added
	// through them. The table is dropped rather than kept — OAuth is the only
	// way into /mcp now, and a stray hash left behind would be a live credential
	// nothing revokes.
	`DROP TABLE api_tokens;`,
	// A dynamically-registered OAuth client (RFC 7591 DCR). redirect_uris is
	// newline-joined because the set is tiny and a join table would be four rows
	// of ceremony for a value only ever read whole. No client secret column: MCP
	// clients register as public (token_endpoint_auth_method=none) and are bound
	// by PKCE, not a secret.
	`CREATE TABLE oauth_clients (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		redirect_uris TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);`,
	// An authorization code is single-use and short-lived; it is stored hashed
	// for the same reason every other credential here is — a leaked database
	// must not hand over a live one. code_challenge is the PKCE S256 challenge,
	// re-checked at the token endpoint. The row is deleted on redemption (see
	// ConsumeAuthorizationCode), so expiry is a backstop for codes never
	// redeemed.
	`CREATE TABLE oauth_authorization_codes (
		code_hash TEXT PRIMARY KEY,
		client_id TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		redirect_uri TEXT NOT NULL,
		code_challenge TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT '',
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);`,
	`CREATE TABLE oauth_access_tokens (
		token_hash TEXT PRIMARY KEY,
		client_id TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		scope TEXT NOT NULL DEFAULT '',
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);`,
	// A refresh token is rotated on every use (ConsumeRefreshToken deletes the
	// old and a fresh pair is minted), so a replayed one finds nothing.
	`CREATE TABLE oauth_refresh_tokens (
		token_hash TEXT PRIMARY KEY,
		client_id TEXT NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		scope TEXT NOT NULL DEFAULT '',
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);`,
	// Indexed on user_id because revoking a user's grants (sign out other
	// devices) deletes by user, and that is the one non-PK access pattern.
	`CREATE INDEX idx_oauth_access_user ON oauth_access_tokens(user_id);`,
	`CREATE INDEX idx_oauth_refresh_user ON oauth_refresh_tokens(user_id);`,
	// A user-set display title that wins over the one the scanner read out of the
	// file. It is a separate column rather than an edit to `title` because the
	// scanner rewrites `title` from ComicInfo.xml on every rescan, so an edit
	// there would revert the moment the file was walked again — the same reason
	// UpsertComic's ON CONFLICT leaves owner_id and source alone. Empty means "no
	// override", so the effective title is COALESCE(NULLIF(title_override,''),title).
	`ALTER TABLE comics ADD COLUMN title_override TEXT NOT NULL DEFAULT '';`,
	// A collection's kind splits the one table into two user-facing concepts:
	// 'collection' (the default) and 'readinglist'. Reading lists are ordered
	// groups too — a comic can sit in many of either — so they share every bit of
	// the collection machinery (ownership, sharing, ordering, covers) and differ
	// only in which page lists them. A discriminator column keeps that one code
	// path instead of a parallel table that would duplicate all of it.
	`ALTER TABLE collections ADD COLUMN kind TEXT NOT NULL DEFAULT 'collection';`,
}

// migrate applies pending migrations inside one transaction. The transaction is
// the point: without it a migration that fails halfway leaves its DDL applied
// but the version unbumped, so the next start replays it against a schema that
// already has half of it and fails forever on "table already exists". SQLite has
// transactional DDL, so the whole batch either lands or does not.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	var current int
	var v string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err == nil {
		if n, err := strconv.Atoi(v); err == nil {
			current = n
		}
	}
	if current >= len(migrations) {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i := current; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('schema_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}
