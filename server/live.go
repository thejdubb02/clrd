package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Live-only messages (#400, ntfy's Cache: no): sent to the streams open
// right now and stored nowhere, so they never reach an inbox, a replay or a
// hook. Only streams that asked for them get them (ntfy subscribe).

var errLiveFile = errf(400, "bad_request", "a live message cannot carry an uploaded file")

// liveID is a live message's id: "l", the log seq at send in 7 base36
// digits, then a 4 digit counter. 12 chars like a ntfy id, unique (ntfy
// apps drop a repeated id), and since=<it> replays what was stored after.
func liveID(seq int64, n uint32) string {
	pad := func(v string, w int) string { return strings.Repeat("0", max(0, w-len(v))) + v }
	return "l" + pad(strconv.FormatInt(seq, 36), 7) + pad(strconv.FormatUint(uint64(n%(36*36*36*36)), 36), 4)
}

// liveSeq is the log seq a live id was sent at.
func liveSeq(id string) (int64, bool) {
	if len(id) != 12 || id[0] != 'l' {
		return 0, false
	}
	n, err := strconv.ParseInt(id[1:8], 36, 64)
	return n, err == nil && n >= 0
}

func (s *Server) publishLive(ctx context.Context, tok *Token, in PublishInput, format string) (*Notification, error) {
	switch {
	case in.file != "":
		return nil, errLiveFile
	case in.SnoozeUntil != nil, in.Repeat != "", in.RepeatTimes != 0, in.Expires != "":
		return nil, errf(400, "bad_request", "a live message goes out now and is not kept: no delay, repeat or expiry")
	case in.ExternalID != "", in.Status != "": // Reopen needs an external_id, so it means nothing here
		return nil, errf(400, "bad_request", "a live message is not kept, so it has no external_id or status to update")
	}
	if err := in.fill(); err != nil {
		return nil, err
	}
	if in.Icon == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT icon FROM topics WHERE topic = ?`, in.Topic).Scan(&in.Icon); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	t := now()
	sils, err := activeSilences(ctx, s.db, t)
	if err != nil {
		return nil, err
	}
	n := &Notification{Topic: in.Topic, Title: in.Title, Message: in.Message, Format: format, Priority: in.Priority,
		Tags: in.Tags, Click: in.Click, Icon: in.Icon, Actions: in.Actions, Attachment: in.Attachment, State: Active,
		StateBy: tok.Name, StateByID: tok.ID, CreatedAt: t, UpdatedAt: t, Live: true}
	n.Silenced = silenced(sils, n)
	s.mu.Lock() // so the seq it carries is in order with stored writes
	defer s.mu.Unlock()
	s.liveN++
	e := Event{Seq: s.seq.Load(), Type: EventCreated, Notification: n}
	n.ID, n.Seq = liveID(e.Seq, s.liveN), e.Seq
	if e.Raw, err = json.Marshal(e); err != nil {
		return nil, errf(400, "bad_request", "cannot encode notification: %v", err)
	}
	s.hub.publish(e)
	return senderView(tok, n, true), nil
}
