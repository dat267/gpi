package tui

import "time"

// Port of the terminal query protocol of upstream's TuiBase: the OSC 11
// background-color query with its pending-query bookkeeping, the `CSI ? 996` /
// `CSI ? 997` color-scheme pair, the listener registries that deliver replies,
// and the `CSI ? 2031` notification toggle (D165).
//
// It is deliberately independent of the renderer: nothing here paints or
// dispatches input, the writer arrives as a one-method seam on each call, and the
// reply path is driven by ConsumeInput. The renderer holds one and forwards its
// own methods to it; tests drive the protocol with a fake writer alone.

// terminalQueryWriter is the write half of a terminal, and the seam the query
// protocol writes through.
type terminalQueryWriter interface {
	Write(data string)
}

// pendingOSC11Query is one in-flight OSC 11 query.
type pendingOSC11Query struct {
	settled bool
	result  chan osc11Result
}

type osc11Result struct {
	color RgbColor
	ok    bool
}

type colorSchemeListener struct {
	id       int
	listener func(TerminalColorScheme)
}

type backgroundListener struct {
	id       int
	listener func(RgbColor)
}

// terminalQueries owns the query protocol's state: the pending OSC 11 queries,
// the reply registries and the notification toggle. Registration and reply
// dispatch both happen on the owner loop, so the registries need no lock.
type terminalQueries struct {
	// started/stopped mirror the terminal's lifecycle: a notification toggle
	// requested before Start only flips the flag, and Start replays it once raw
	// mode is active, so the mode sequence is never echoed into the input stream
	// (the SSH-launch freeze, D165).
	started bool
	stopped bool
	notify  bool

	pendingOSC11Replies  int
	pendingOSC11Queries  []*pendingOSC11Query
	colorSchemeListeners []colorSchemeListener
	nextColorSchemeID    int
	backgroundListeners  []backgroundListener
	nextBackgroundID     int
}

// OnBackgroundChange subscribes to OSC 11 background-color replies. The listener
// runs on the owner loop (input dispatch); registration happens during
// setup/teardown only, so the registry needs no lock.
func (q *terminalQueries) OnBackgroundChange(listener func(RgbColor)) func() {
	q.nextBackgroundID++
	id := q.nextBackgroundID
	q.backgroundListeners = append(q.backgroundListeners, backgroundListener{id: id, listener: listener})
	return func() {
		filtered := q.backgroundListeners[:0]
		for _, entry := range q.backgroundListeners {
			if entry.id != id {
				filtered = append(filtered, entry)
			}
		}
		q.backgroundListeners = filtered
	}
}

// OnColorSchemeChange subscribes to terminal color-scheme reports.
func (q *terminalQueries) OnColorSchemeChange(listener func(TerminalColorScheme)) func() {
	q.nextColorSchemeID++
	id := q.nextColorSchemeID
	q.colorSchemeListeners = append(q.colorSchemeListeners, colorSchemeListener{id: id, listener: listener})
	return func() {
		filtered := q.colorSchemeListeners[:0]
		for _, entry := range q.colorSchemeListeners {
			if entry.id != id {
				filtered = append(filtered, entry)
			}
		}
		q.colorSchemeListeners = filtered
	}
}

// RequestBackground writes the OSC 11 query and returns. It never blocks: the
// reply is delivered to the OnBackgroundChange listeners on the owner loop.
// Callers must invoke it only after the terminal is in raw mode and the input
// reader is live, so the reply is dispatched rather than echoed (D165).
func (q *terminalQueries) RequestBackground(writer terminalQueryWriter) {
	if writer == nil || q.stopped {
		return
	}
	writer.Write("\x1b]11;?\x07")
}

// QueryBackground queries the terminal's background color with OSC 11. It returns
// ok=false on timeout or an unparsable reply.
func (q *terminalQueries) QueryBackground(writer terminalQueryWriter, timeoutMS int) (RgbColor, bool) {
	query := &pendingOSC11Query{result: make(chan osc11Result, 1)}
	q.pendingOSC11Queries = append(q.pendingOSC11Queries, query)
	q.pendingOSC11Replies++
	if writer == nil {
		return RgbColor{}, false
	}
	writer.Write("\x1b]11;?\x07")

	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case result := <-query.result:
		return result.color, result.ok
	case <-timer.C:
		if !query.settled {
			query.settled = true
		}
		return RgbColor{}, false
	}
}

// QueryColorScheme queries the terminal's color-scheme preference with DSR
// (`CSI ? 996 n`).
func (q *terminalQueries) QueryColorScheme(writer terminalQueryWriter, timeoutMS int) (TerminalColorScheme, bool) {
	results := make(chan TerminalColorScheme, 1)
	unsubscribe := q.OnColorSchemeChange(func(scheme TerminalColorScheme) {
		select {
		case results <- scheme:
		default:
		}
	})
	defer unsubscribe()
	if writer == nil {
		return "", false
	}
	writer.Write("\x1b[?996n")

	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case scheme := <-results:
		return scheme, true
	case <-timer.C:
		return "", false
	}
}

// SetNotify enables the `CSI ? 2031` notifications.
func (q *terminalQueries) SetNotify(writer terminalQueryWriter, enabled bool) {
	if q.notify == enabled {
		return
	}
	q.notify = enabled
	// Before the terminal is started the request only flips the flag: Start
	// replays it once raw mode is active, so the sequence is never echoed into
	// the input stream (the SSH-launch freeze).
	if !q.started {
		return
	}
	if q.stopped || writer == nil {
		return
	}
	if enabled {
		writer.Write("\x1b[?2031h")
	} else {
		writer.Write("\x1b[?2031l")
	}
}

// NotifyOnStart replays a notification toggle requested before the terminal was
// started, and marks it started.
func (q *terminalQueries) NotifyOnStart(writer terminalQueryWriter) {
	q.started = true
	if q.notify && writer != nil {
		writer.Write("\x1b[?2031h")
	}
}

// NotifyOnStop disables an enabled notification and marks the terminal stopped.
func (q *terminalQueries) NotifyOnStop(writer terminalQueryWriter) {
	started := q.started
	q.started = false
	q.stopped = true
	if started && q.notify {
		q.notify = false
		if writer != nil {
			writer.Write("\x1b[?2031l")
		}
	}
}

// ConsumeInput resolves an OSC 11 reply or a color-scheme report out of the input
// stream, reporting whether it consumed it. An OSC 11 reply is never user input,
// so it is consumed even when no query is pending; otherwise a proactive probe's
// reply would leak into the editor.
func (q *terminalQueries) ConsumeInput(data string) bool {
	if IsOsc11BackgroundColorResponse(data) {
		q.resolveBackgroundResponse(data)
		return true
	}
	scheme, ok := ParseTerminalColorSchemeReport(data)
	if !ok {
		return false
	}
	listeners := append([]colorSchemeListener{}, q.colorSchemeListeners...)
	for _, entry := range listeners {
		entry.listener(scheme)
	}
	return true
}

// resolveBackgroundResponse resolves the oldest pending OSC 11 query and notifies
// the background listeners.
func (q *terminalQueries) resolveBackgroundResponse(data string) {
	color, ok := ParseOsc11BackgroundColor(data)

	if q.pendingOSC11Replies > 0 {
		q.pendingOSC11Replies--
		var query *pendingOSC11Query
		if len(q.pendingOSC11Queries) > 0 {
			query = q.pendingOSC11Queries[0]
			q.pendingOSC11Queries = q.pendingOSC11Queries[1:]
		}
		if query != nil && !query.settled {
			query.settled = true
			select {
			case query.result <- osc11Result{color: color, ok: ok}:
			default:
			}
		}
	}

	if ok && len(q.backgroundListeners) > 0 {
		listeners := append([]backgroundListener{}, q.backgroundListeners...)
		for _, entry := range listeners {
			entry.listener(color)
		}
	}
}
