// Package api holds every type that crosses the wire. It is the single source
// of truth for the TypeScript client: `go generate ./...` runs tygo over this
// package and writes web/src/api/generated.ts. Never hand-edit that file.
//
// Keep internal-only shapes out of this package so they can't leak into the
// client. Timestamps are Unix seconds as int64 (tygo maps them to number).
package api

//go:generate go run github.com/gzuidhof/tygo@v0.2.17 generate

// APIError is the body of every non-2xx response. The client surfaces .error
// verbatim, so the text must be safe to show a user.
type APIError struct {
	Error string `json:"error"`
}

// User is an account. Passkey-only; no password is ever stored.
type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsAdmin   bool   `json:"isAdmin"`
	CreatedAt int64  `json:"createdAt"`
}

// Credential is an enrolled passkey, as shown on the account page.
type Credential struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
	LastUsed  int64  `json:"lastUsed,omitempty"`
}

// Session is the response of GET /auth/me. A logged-out caller gets 401, which
// the client maps to null rather than an error.
type Session struct {
	User        User         `json:"user"`
	Credentials []Credential `json:"credentials"`
}

// Invite is a single-use enrollment link. ForUser set means the link enrolls an
// additional passkey onto an existing account instead of creating one.
type Invite struct {
	Token       string `json:"token"`
	CreatedAt   int64  `json:"createdAt"`
	ExpiresAt   int64  `json:"expiresAt"`
	CreatedBy   string `json:"createdBy,omitempty"`
	ForUser     string `json:"forUser,omitempty"`
	ForUserName string `json:"forUserName,omitempty"`
	IsAdmin     bool   `json:"isAdmin"`
}

// EnrollRequest redeems an invite link. Name is ignored for a recovery invite,
// which already knows the account it belongs to.
type EnrollRequest struct {
	Token string `json:"token"`
	Name  string `json:"name"`
}

// CreateInviteRequest mints a new-user invite.
type CreateInviteRequest struct {
	IsAdmin bool `json:"isAdmin"`
}

// Comic is one CBZ in the library.
//
// Path is relative to the library root and is the stable identity across
// rescans: the watcher matches on it first and falls back to content hash so a
// plain rename keeps reading progress and tags attached.
type Comic struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	Series    string `json:"series,omitempty"`
	Number    string `json:"number,omitempty"`
	Volume    string `json:"volume,omitempty"`
	Summary   string `json:"summary,omitempty"`
	PageCount int    `json:"pageCount"`
	FileSize  int64  `json:"fileSize"`
	AddedAt   int64  `json:"addedAt"`
	ModifiedAt int64 `json:"modifiedAt"`
	// Missing marks a comic whose file vanished. Rows are kept rather than
	// deleted so an unmounted volume doesn't silently destroy tags and
	// progress; the sweep clears the flag if the file comes back.
	Missing bool     `json:"missing"`
	Tags    []string `json:"tags"`
}

// Page is one image inside a CBZ. Name is the zip entry name.
type Page struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// ComicDetail is the reader's payload: the comic plus its page list and the
// caller's own progress.
type ComicDetail struct {
	Comic    Comic    `json:"comic"`
	Pages    []Page   `json:"pages"`
	Progress *Progress `json:"progress,omitempty"`
}

// Progress is one user's position in one comic. Page is 0-based.
type Progress struct {
	ComicID   string `json:"comicId"`
	Page      int    `json:"page"`
	PageCount int    `json:"pageCount"`
	Completed bool   `json:"completed"`
	UpdatedAt int64  `json:"updatedAt"`
}

// Tag is a free-form label. Tags are server-global rather than per-user: a
// small trusted instance benefits more from everyone seeing the same
// vocabulary than from per-user namespacing.
type Tag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Collection is an ordered, user-owned set of comics.
type Collection struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Summary   string `json:"summary,omitempty"`
	OwnerID   string `json:"ownerId"`
	OwnerName string `json:"ownerName,omitempty"`
	// Shared exposes the collection to every other user on the server. Off by
	// default: an uploader opts in per collection, never in bulk.
	Shared    bool  `json:"shared"`
	Count     int   `json:"count"`
	CreatedAt int64 `json:"createdAt"`
	// CoverComicID is the comic whose cover represents the collection.
	CoverComicID string `json:"coverComicId,omitempty"`
}

type CreateCollectionRequest struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	Shared  bool   `json:"shared"`
}

type UpdateCollectionRequest struct {
	Name    *string `json:"name,omitempty"`
	Summary *string `json:"summary,omitempty"`
	Shared  *bool   `json:"shared,omitempty"`
}

type SetTagsRequest struct {
	Tags []string `json:"tags"`
}

type ProgressRequest struct {
	Page      int  `json:"page"`
	Completed bool `json:"completed"`
}

// ImportStage names the phase of an import job. The UI shows these verbatim.
type ImportStage string

const (
	StageUploading ImportStage = "uploading"
	// StageReading covers one combined pass: each image is read, hashed and
	// thumbnailed in a single decode. The pipeline does not decode twice, so
	// there is no separate thumbnailing stage to report.
	StageReading  ImportStage = "reading"
	StageGrouping ImportStage = "grouping"
	StageEncoding  ImportStage = "encoding"
	StagePackaging ImportStage = "packaging"
	StageDone      ImportStage = "done"
	StageFailed    ImportStage = "failed"
)

// ImportJob tracks a folder-of-images import through dedupe to a finished CBZ.
// Jobs are persisted because a large import outlives any one request and must
// survive a restart well enough to report what happened.
type ImportJob struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	OwnerID   string      `json:"ownerId"`
	Stage     ImportStage `json:"stage"`
	// Done/Total drive the progress bar. Total is 0 while it is still unknown.
	Done  int `json:"done"`
	Total int `json:"total"`
	// Counts filled in as the pipeline learns them.
	SourceCount int    `json:"sourceCount"`
	PageCount   int    `json:"pageCount"`
	ExactDupes  int    `json:"exactDupes"`
	NearDupes   int    `json:"nearDupes"`
	Message     string `json:"message,omitempty"`
	ComicID     string `json:"comicId,omitempty"`
	StartedAt   int64  `json:"startedAt"`
	FinishedAt  int64  `json:"finishedAt,omitempty"`
}

// ImportOptions mirrors package.py's flags. Defaults live in the Go zero value
// handling, not here, so an omitted field means "server default".
type ImportOptions struct {
	Name string `json:"name"`
	// Threshold is the mean-absolute-error ceiling for calling two pages the
	// same image, on a 0-255 scale. 3.0 is the empirically derived default:
	// on real galleries duplicate pairs land under ~2.2 and the nearest
	// distinct pair is an order of magnitude above it.
	Threshold float64 `json:"threshold"`
	// Exact skips pixel grouping and dedupes on SHA-256 only.
	Exact bool `json:"exact"`
	// Encode is "", "avif", "webp" or "jpeg". Empty copies pages verbatim.
	Encode  string `json:"encode,omitempty"`
	Quality int    `json:"quality,omitempty"`
	// CollectionID optionally files the finished comic into a collection.
	CollectionID string `json:"collectionId,omitempty"`
}

// DupeGroup is one cluster from the dedupe report: the page that was kept plus
// the ones it stood in for.
type DupeGroup struct {
	Kept    string   `json:"kept"`
	Dropped []string `json:"dropped"`
	Reason  string   `json:"reason"`
}

// LibraryStatus is what the scanner is currently doing.
type LibraryStatus struct {
	Scanning  bool   `json:"scanning"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
	LastScan  int64  `json:"lastScan,omitempty"`
	ComicCount int   `json:"comicCount"`
	Root      string `json:"root"`
}

// WSType discriminates a WSMessage. The client switches on it.
type WSType string

const (
	WSTypeLibrary WSType = "library"
	WSTypeComics  WSType = "comics"
	WSTypeJobs    WSType = "jobs"
	WSTypeJob     WSType = "job"
)

// WSMessage is the push envelope. Payload fields are mutually exclusive
// pointers so tygo emits them as optional and the client can discriminate on
// type.
//
// Jobs carries the complete job set on connect rather than a delta. That is
// what lets a reconnecting client clear a spinner for a job the server has
// since forgotten; without it the spinner outlives the job forever.
type WSMessage struct {
	Type    WSType         `json:"type"`
	Library *LibraryStatus `json:"library,omitempty"`
	Comics  []Comic        `json:"comics,omitempty"`
	Jobs    []ImportJob    `json:"jobs,omitempty"`
	Job     *ImportJob     `json:"job,omitempty"`
}
