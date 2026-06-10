package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// The Emit relay. spec.Emit computes a message (target, text); the runtime
// collects them in Output.Emits; WriteInbox delivers each one.
//
// This is a stull-defined LOCAL inbox, not bus delivery. It writes one atomic
// JSON file per message under {dir}/{target}/ — a documented on-disk format that
// a separate adapter can forward onto a real bus (e.g. mcp-dispatch) once that
// contract is known. Calling this "onto the bus" would overclaim: it is a local
// artifact that represents a message, not a live transport. Nothing leaves the
// host until an adapter (not built here) moves it.
type inboxMessage struct {
	From    string `json:"from"`
	Target  string `json:"target"`
	Message string `json:"message"`
	TS      string `json:"ts"`
}

var inboxSeq atomic.Uint64

// WriteInbox atomically writes one emission into a local inbox. It is fail-open:
// errors are returned for the caller to ignore, never to disturb a hook. target
// is collapsed to a single path segment so an Emit can never write outside dir.
func WriteInbox(dir, from string, e Emission) error {
	target := filepath.Base(filepath.Clean(e.Target))
	if target == "." || target == ".." || target == string(filepath.Separator) {
		return os.ErrInvalid
	}
	tdir := filepath.Join(dir, target)
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		return err
	}

	body, err := json.Marshal(inboxMessage{
		From: from, Target: e.Target, Message: e.Message, TS: now(),
	})
	if err != nil {
		return err
	}

	// Unique name: nanosecond clock + a process-local sequence so two emissions
	// in the same tick can't collide.
	name := time.Now().UTC().Format("20060102T150405.000000000Z") +
		"-" + strconv.FormatUint(inboxSeq.Add(1), 10) + ".json"
	final := filepath.Join(tdir, name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final) // atomic publish
}
