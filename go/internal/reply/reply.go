// Package reply is the human's answer to a mailbox message: `orch mailbox reply` and a Work
// Trains answer pulled from the phone both go through Post, so a page answer lands exactly as a
// terminal one does. It lives outside package mailbox because answering the human gate's
// question clears the gate through flipcmd, and flipcmd already imports mailbox.
package reply

import (
	"errors"

	"github.com/youthtrouble/opencode-orchestrator/go/internal/flipcmd"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsjson"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/jsstr"
	"github.com/youthtrouble/opencode-orchestrator/go/internal/mailbox"
)

// human is the mailbox name the reply is posted and acknowledged as.
const human = "human"

// subjectUnits is how many UTF-16 units of "Re: <subject>" the reply's subject keeps.
const subjectUnits = 140

// Ack notes: a terminal reply, and one pulled from Work Trains.
const (
	terminalAckNote = "replied"
	remoteAckNote   = "replied via Work Trains"
)

// Request is a reply to the message ID (a full id or a unique prefix) with Body. A reply pulled
// from Work Trains carries Remote, which marks the posted line `"via": "remote"` with the
// answer's identity (the key a retried pull is recognised by) and changes the ack note; a
// terminal reply leaves it nil and is written exactly as bin/mailbox.mjs writes it.
type Request struct {
	ID     string
	Body   string
	Remote *mailbox.RemoteRef
}

// Gate is what a reply did to the human gate.
type Gate int

const (
	// GateUntouched: there is no gate, or its question is another message.
	GateUntouched Gate = iota
	// GateCleared: the gate is cleared and the supervisor relaunches Result.Door.
	GateCleared
	// GateClearedWithoutRelaunch: the gate is cleared but the kept pending_launch names no door.
	GateClearedWithoutRelaunch
	// GateUnreadable: flip.json exists but could not be read or parsed (Result.GateErr).
	GateUnreadable
	// GateUnmatched: the gate's question resolves to no single message (Result.GateErr).
	GateUnmatched
	// GateClearFailed: the reply answered the gate but flip.json could not be written
	// (Result.GateErr).
	GateClearFailed
)

// Result is a posted reply: the full id it answers, the reply's own id and what happened to the
// human gate. An unreadable gate returns an error even when the note was already posted.
type Result struct {
	ID      string
	ReplyID string
	Gate    Gate
	Door    any // the door relaunched, when Gate is GateCleared
	GateErr error
}

// Post resolves r.ID, posts the human's note to the original's sender (`*` when the original
// is gone or has no sender) threaded on it and finishes the reply (Finish). An error means
// nothing was posted, unless the ack or gate check failed after posting the note.
func Post(box *mailbox.Mailbox, project flipcmd.Project, r Request) (Result, error) {
	id, err := box.ResolveID(r.ID)
	if err != nil {
		return Result{}, err
	}
	all, err := box.List(mailbox.Query{Limit: 100000})
	if err != nil {
		return Result{}, err
	}
	to, subject := addressOf(findByID(all, id), id)
	note, err := box.Post(mailbox.Draft{
		From:    human,
		To:      to,
		Kind:    "note",
		Subject: jsstr.Slice("Re: "+subject, 0, subjectUnits),
		Body:    r.Body,
		Thread:  id,
		Remote:  r.Remote,
	})
	if err != nil {
		return Result{}, err
	}
	result, err := Finish(box, project, id, r.Remote, false)
	result.ReplyID, _ = jsjson.Path(note, "id").(string)
	return result, err
}

// Finish is what follows a posted reply to the message id: the human's ack of it (skipped when
// ackedByHuman says the human already has) and, when id is the human gate's question, clearing
// the gate and queueing the gated conductor's relaunch. Both steps are idempotent, so a reply
// whose note survived a crash before them can be finished again. Result.ReplyID is left empty.
func Finish(box *mailbox.Mailbox, project flipcmd.Project, id string, remote *mailbox.RemoteRef, ackedByHuman bool) (Result, error) {
	if !ackedByHuman {
		who := human
		if _, err := box.Ack(mailbox.AckRequest{ID: &id, By: &who, Note: ackNote(remote)}); err != nil {
			return Result{}, err
		}
	}
	result := Result{ID: id}
	result.Gate, result.Door, result.GateErr = clearGateAnsweredBy(box, project, id)
	if result.Gate == GateUnreadable {
		return result, result.GateErr
	}
	return result, nil
}

// findByID is the message whose id is exactly id, or nil.
func findByID(msgs []*jsjson.Object, id string) *jsjson.Object {
	for _, m := range msgs {
		if s, _ := jsjson.Path(m, "id").(string); s == id {
			return m
		}
	}
	return nil
}

// addressOf is who the reply goes to and the subject it answers: the original's truthy `from`
// (whatever its JSON shape) and its subject as text, else `*` and the id.
func addressOf(orig *jsjson.Object, id string) (to any, subject string) {
	if orig == nil {
		return "*", id
	}
	to = "*"
	if from := jsjson.Path(orig, "from"); jsjson.Truthy(from) {
		to = from
	}
	return to, jsjson.OrText(jsjson.Path(orig, "subject"), id)
}

func ackNote(remote *mailbox.RemoteRef) string {
	if remote != nil {
		return remoteAckNote
	}
	return terminalAckNote
}

// clearGateAnsweredBy clears the human gate when id answers it: the gate's question (stored as
// a full id or a prefix) resolves to that same message, the way the reply's own id was
// resolved. A question that resolves to no single message answers nothing.
func clearGateAnsweredBy(box *mailbox.Mailbox, project flipcmd.Project, id string) (Gate, any, error) {
	result := GateUntouched
	var door any
	err := project.Update(func(prev flipcmd.Record) (any, error) {
		question, _ := jsjson.Path(prev.Value, "awaiting_human", "question").(string)
		if question == "" {
			return nil, flipcmd.ErrNoUpdate
		}
		gateID, err := box.ResolveID(question)
		if err != nil {
			result = GateUnmatched
			return nil, err
		}
		if gateID != id {
			return nil, flipcmd.ErrNoUpdate
		}
		next, d, queued := project.ClearGateRecord(prev.Value, true)
		door = d
		result = GateClearedWithoutRelaunch
		if queued {
			result = GateCleared
		}
		return next, nil
	})
	if err != nil {
		var stateErr *flipcmd.StateError
		if errors.As(err, &stateErr) {
			return GateUnreadable, nil, err
		}
		if result == GateUnmatched {
			return result, nil, err
		}
		return GateClearFailed, nil, err
	}
	return result, door, nil
}
