package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ErrObjectNotFound is returned by Storage.Open for a name that is not stored.
var ErrObjectNotFound = errors.New("backup: object not found")

// Storage is where backup files live: a folder or an S3-compatible bucket.
// Names are flat, without path separators.
type Storage interface {
	// Put stores r under name and returns the number of bytes stored. A
	// failed Put leaves no object behind.
	Put(ctx context.Context, name string, r io.Reader) (int64, error)
	// Open returns the object's bytes, or an error wrapping ErrObjectNotFound.
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	// List returns the backup objects (*.pxbak), newest first. It reads
	// only each object's plaintext header, never the encrypted payload.
	List(ctx context.Context) ([]Object, error)
	// Delete removes an object. Deleting a missing object is not an error.
	Delete(ctx context.Context, name string) error
	// Describe is a human label for the location; it never carries
	// credentials.
	Describe() string
}

// PartialCleaner is implemented by a Storage that can leave half-written
// files behind when a backup is stopped mid-upload. The service calls it at
// the start of every backup, holding the backup lock, so no upload of this
// instance is in flight. An object store discards an unfinished upload by
// itself and does not implement it.
type PartialCleaner interface {
	// CleanPartials removes the half-written files and returns how many it
	// removed.
	CleanPartials(ctx context.Context) (int, error)
}

// Object is one listed backup file.
type Object struct {
	Name string
	Size int64
	// Header is valid when HeaderErr is nil.
	Header Header
	// HeaderErr is why the header could not be read: the file is not a
	// backup, or is damaged. Such an object is still listed.
	HeaderErr error
	// Verified is set by MarkVerified: the header is authentic under this
	// instance's header key and agrees with the object's name. Only a
	// verified object is known to be a backup this instance wrote.
	Verified bool
}

// created is the object's creation time and kind, from the header when it
// parsed, otherwise from the name. ok is false when neither knows.
func (o Object) created() (at time.Time, kind string, ok bool) {
	if o.HeaderErr == nil {
		return o.Header.CreatedAt, o.Header.Kind, true
	}
	return ParseObjectName(o.Name)
}

// sortNewestFirst orders objects by creation time, newest first; objects
// whose time is unknown go last, and ties fall back to the name.
func sortNewestFirst(objs []Object) {
	sort.SliceStable(objs, func(i, j int) bool {
		ai, _, oki := objs[i].created()
		aj, _, okj := objs[j].created()
		switch {
		case oki != okj:
			return oki
		case !ai.Equal(aj):
			return ai.After(aj)
		default:
			return objs[i].Name > objs[j].Name
		}
	})
}

// validName rejects names that could leave the storage root or hide.
func validName(name string) error {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("backup: invalid object name %q", name)
	}
	return nil
}

// headerObject builds the listing entry for a stored object by reading the
// header from its first bytes.
func headerObject(name string, size int64, head io.Reader) Object {
	o := Object{Name: name, Size: size}
	o.Header, o.HeaderErr = ReadHeader(head)
	return o
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Probe checks that s can be written, listed, read and deleted, by storing a
// small throwaway object and removing it again. Listings only show *.pxbak
// objects, so the probe object itself is verified by reading it back.
func Probe(ctx context.Context, s Storage) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("probe storage: %w", err)
	}
	name := "pulsix-probe-" + hex.EncodeToString(rnd[:]) + ".tmp"
	payload := []byte("pulsix storage probe " + hex.EncodeToString(rnd[:]))

	if _, err := s.Put(ctx, name, bytes.NewReader(payload)); err != nil {
		return fmt.Errorf("probe storage: write: %w", err)
	}
	// Whatever happens next, do not leave the probe object behind.
	deleted := false
	defer func() {
		if !deleted {
			_ = s.Delete(context.WithoutCancel(ctx), name)
		}
	}()

	if _, err := s.List(ctx); err != nil {
		return fmt.Errorf("probe storage: list: %w", err)
	}
	rc, err := s.Open(ctx, name)
	if err != nil {
		return fmt.Errorf("probe storage: read: %w", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("probe storage: read: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return errors.New("probe storage: read back different bytes than were written")
	}
	if err := s.Delete(ctx, name); err != nil {
		return fmt.Errorf("probe storage: delete: %w", err)
	}
	deleted = true
	return nil
}
