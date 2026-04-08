package skylive_rt

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FirestoreStore is a persistent session store backed by Google Cloud Firestore.
// It implements the SessionStore interface using optimistic concurrency via
// Firestore's transaction support.
//
// Configuration:
//   - storePath can be:
//     - A path to a service account JSON file (e.g., "firebaseadminsdk.json")
//     - A Firestore project ID prefixed with "project:" (e.g., "project:my-project")
//     - Empty string to use Application Default Credentials
//
// The store uses a "sky_sessions" collection with documents keyed by session ID.
type FirestoreStore struct {
	client     *firestore.Client
	collection string
	ttl        time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
}

// sessionDoc is the Firestore document structure for sessions.
type sessionDoc struct {
	ModelJSON    string `firestore:"model_json"`
	PrevViewHTML string `firestore:"prev_view_html"`
	CreatedAt    int64  `firestore:"created_at"`
	LastSeen     int64  `firestore:"last_seen"`
	Version      int64  `firestore:"version"`
}

// NewFirestoreStore creates a new Firestore-backed session store.
// storePath is either a path to a service account JSON file, a "project:ID"
// string, or empty to use Application Default Credentials.
func NewFirestoreStore(storePath string, ttl time.Duration) (*FirestoreStore, error) {
	ctx := context.Background()

	var app *firebase.App
	var err error

	if storePath == "" {
		// Use Application Default Credentials
		app, err = firebase.NewApp(ctx, nil)
	} else if len(storePath) > 8 && storePath[:8] == "project:" {
		// Use project ID with ADC
		projectID := storePath[8:]
		app, err = firebase.NewApp(ctx, &firebase.Config{ProjectID: projectID})
	} else {
		// Use service account JSON file
		opt := option.WithCredentialsFile(storePath)
		app, err = firebase.NewApp(ctx, nil, opt)
	}
	if err != nil {
		return nil, err
	}

	client, err := app.Firestore(ctx)
	if err != nil {
		return nil, err
	}

	// Create a cancellable context for the store
	ctx, cancel := context.WithCancel(ctx)

	store := &FirestoreStore{
		client:     client,
		collection: "sky_sessions",
		ttl:        ttl,
		ctx:        ctx,
		cancel:     cancel,
	}

	// Start background cleanup goroutine
	go store.cleanup()

	return store, nil
}

// Get loads a session from Firestore.
func (s *FirestoreStore) Get(sid string) (*Session, bool) {
	doc, err := s.client.Collection(s.collection).Doc(sid).Get(s.ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, false
		}
		log.Printf("skylive_rt: FirestoreStore.Get: %v", err)
		return nil, false
	}

	var sd sessionDoc
	if err := doc.DataTo(&sd); err != nil {
		log.Printf("skylive_rt: FirestoreStore.Get: failed to unmarshal: %v", err)
		return nil, false
	}

	// Deserialise model from JSON
	var model map[string]any
	if err := json.Unmarshal([]byte(sd.ModelJSON), &model); err != nil {
		log.Printf("skylive_rt: FirestoreStore.Get: failed to unmarshal model: %v", err)
		return nil, false
	}
	fixJSONNumbers(model)

	// Deserialise previous view tree
	var prevView *VNode
	if sd.PrevViewHTML != "" {
		prevView = ParseHTML(sd.PrevViewHTML)
	}

	sess := &Session{
		Model:    model,
		PrevView: prevView,
		Created:  time.Unix(sd.CreatedAt, 0),
		LastSeen: time.Unix(sd.LastSeen, 0),
		Version:  sd.Version,
	}

	// Touch last_seen
	now := time.Now()
	sess.LastSeen = now
	s.client.Collection(s.collection).Doc(sid).Set(s.ctx, map[string]any{
		"last_seen": now.Unix(),
	}, firestore.MergeAll)

	return sess, true
}

// Set serialises the session and writes it to Firestore using optimistic
// concurrency control. Returns false on version conflict.
func (s *FirestoreStore) Set(sid string, sess *Session) bool {
	now := time.Now()
	sess.LastSeen = now

	modelBytes, err := json.Marshal(sess.Model)
	if err != nil {
		log.Printf("skylive_rt: FirestoreStore.Set: failed to marshal model: %v", err)
		return false
	}

	var prevViewHTML string
	if sess.PrevView != nil {
		prevViewHTML = RenderToString(sess.PrevView)
	}

	createdAt := sess.Created.Unix()
	if createdAt == 0 {
		createdAt = now.Unix()
	}

	newVersion := sess.Version + 1

	// Use a transaction for optimistic concurrency
	err = s.client.RunTransaction(s.ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		docRef := s.client.Collection(s.collection).Doc(sid)
		doc, err := tx.Get(docRef)

		if sess.Version == 0 {
			// New session — create if not exists
			if err != nil {
				// Document doesn't exist, create it
				return tx.Set(docRef, sessionDoc{
					ModelJSON:    string(modelBytes),
					PrevViewHTML: prevViewHTML,
					CreatedAt:    createdAt,
					LastSeen:     now.Unix(),
					Version:      newVersion,
				})
			}
			// Document exists but we're version 0 — conflict
			return status.Error(codes.AlreadyExists, "session already exists")
		}

		// Existing session — check version
		if err != nil {
			return err // document should exist
		}
		var existing sessionDoc
		if err := doc.DataTo(&existing); err != nil {
			return err
		}
		if existing.Version != sess.Version {
			return status.Error(codes.Aborted, "version conflict")
		}

		return tx.Set(docRef, sessionDoc{
			ModelJSON:    string(modelBytes),
			PrevViewHTML: prevViewHTML,
			CreatedAt:    createdAt,
			LastSeen:     now.Unix(),
			Version:      newVersion,
		})
	})

	if err != nil {
		if status.Code(err) == codes.Aborted || status.Code(err) == codes.AlreadyExists {
			return false // version conflict
		}
		log.Printf("skylive_rt: FirestoreStore.Set: transaction failed: %v", err)
		return false
	}

	sess.Version = newVersion
	return true
}

// Delete removes a session from Firestore.
func (s *FirestoreStore) Delete(sid string) {
	_, err := s.client.Collection(s.collection).Doc(sid).Delete(s.ctx)
	if err != nil {
		log.Printf("skylive_rt: FirestoreStore.Delete: %v", err)
	}
}

// NewID generates a cryptographically random session identifier.
func (s *FirestoreStore) NewID() string {
	return generateSessionID()
}

// cleanup periodically deletes expired sessions from Firestore.
func (s *FirestoreStore) cleanup() {
	ticker := time.NewTicker(5 * time.Minute) // Less frequent than in-memory stores
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-s.ttl).Unix()
			iter := s.client.Collection(s.collection).
				Where("last_seen", "<", cutoff).
				Limit(100). // Process in batches to avoid timeouts
				Documents(s.ctx)

			batch := s.client.Batch()
			count := 0
			for {
				doc, err := iter.Next()
				if err != nil {
					break
				}
				batch.Delete(doc.Ref)
				count++
			}
			iter.Stop()

			if count > 0 {
				_, err := batch.Commit(s.ctx)
				if err != nil {
					log.Printf("skylive_rt: FirestoreStore.cleanup: %v", err)
				} else {
					log.Printf("skylive_rt: FirestoreStore.cleanup: removed %d expired sessions", count)
				}
			}
		case <-s.ctx.Done():
			return
		}
	}
}

// Close stops the cleanup goroutine and closes the Firestore client.
func (s *FirestoreStore) Close() error {
	s.cancel()
	return s.client.Close()
}
