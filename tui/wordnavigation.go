package tui

// Port of src/undo-stack.ts, src/kill-ring.ts, and src/word-navigation.ts.

// UndoStack stores state snapshots. Upstream clones on push with
// structuredClone; Go callers pass already-detached values, and the stack
// stores them as-is (divergence D59: no deep clone).
type UndoStack[S any] struct {
	stack []S
}

// Push records a snapshot.
func (s *UndoStack[S]) Push(state S) { s.stack = append(s.stack, state) }

// Pop returns the most recent snapshot.
func (s *UndoStack[S]) Pop() (S, bool) {
	var zero S
	if len(s.stack) == 0 {
		return zero, false
	}
	state := s.stack[len(s.stack)-1]
	s.stack = s.stack[:len(s.stack)-1]
	return state, true
}

// Clear drops all snapshots.
func (s *UndoStack[S]) Clear() { s.stack = nil }

// Len returns the snapshot count.
func (s *UndoStack[S]) Len() int { return len(s.stack) }

// KillRing is a ring buffer for Emacs-style kill/yank operations.
type KillRing struct {
	ring []string
}

// KillRingPushOptions configure a push.
type KillRingPushOptions struct {
	// Prepend selects prepend (backward deletion) versus append (forward).
	Prepend bool
	// Accumulate merges with the most recent entry.
	Accumulate bool
}

// Push adds killed text to the ring.
func (k *KillRing) Push(text string, options KillRingPushOptions) {
	if text == "" {
		return
	}
	if options.Accumulate && len(k.ring) > 0 {
		last := k.ring[len(k.ring)-1]
		k.ring = k.ring[:len(k.ring)-1]
		if options.Prepend {
			k.ring = append(k.ring, text+last)
		} else {
			k.ring = append(k.ring, last+text)
		}
		return
	}
	k.ring = append(k.ring, text)
}

// Peek returns the most recent entry without modifying the ring.
func (k *KillRing) Peek() (string, bool) {
	if len(k.ring) == 0 {
		return "", false
	}
	return k.ring[len(k.ring)-1], true
}

// Rotate moves the last entry to the front (for yank-pop cycling).
func (k *KillRing) Rotate() {
	if len(k.ring) > 1 {
		last := k.ring[len(k.ring)-1]
		k.ring = append([]string{last}, k.ring[:len(k.ring)-1]...)
	}
}

// Len returns the ring size.
func (k *KillRing) Len() int { return len(k.ring) }

// PunctuationRegex matches the ASCII punctuation set used for word
// boundaries (upstream PUNCTUATION_REGEX).
var punctuationChars = map[rune]bool{
	'(': true, ')': true, '{': true, '}': true, '[': true, ']': true, '<': true, '>': true,
	'.': true, ',': true, ';': true, ':': true, '\'': true, '"': true, '!': true, '?': true,
	'+': true, '-': true, '=': true, '*': true, '/': true, '\\': true, '|': true, '&': true,
	'%': true, '^': true, '$': true, '#': true, '@': true, '~': true, '`': true,
}

// WordSegment is a word-granularity segment.
type WordSegment struct {
	Segment    string
	Index      int
	IsWordLike bool
}

// WordSegments segments text with word granularity.
//
// Upstream uses Intl.Segmenter (full UAX #29 word segmentation); the Go port
// implements the practical rules observed from ICU (divergence D58): word
// characters are letters, digits, marks, and connector punctuation; a single
// mid-word "." merges between alphanumerics, "'" between letters, and ","
// between digits; whitespace runs and individual punctuation cells are
// non-word segments.
func WordSegments(text string) []WordSegment {
	runes := []rune(text)
	byteOffsets := make([]int, len(runes)+1)
	offset := 0
	for i, r := range runes {
		byteOffsets[i] = offset
		offset += len(string(r))
	}
	byteOffsets[len(runes)] = offset

	var segments []WordSegment
	index := 0

	for index < len(runes) {
		r := runes[index]

		if isWhitespaceRune(r) {
			start := index
			for index < len(runes) && isWhitespaceRune(runes[index]) {
				index++
			}
			segments = append(segments, WordSegment{Segment: string(runes[start:index]), Index: byteOffsets[start], IsWordLike: false})
			continue
		}

		if !isWordChar(r) {
			segments = append(segments, WordSegment{Segment: string(r), Index: byteOffsets[index], IsWordLike: false})
			index++
			continue
		}

		start := index
		index++
		for index < len(runes) {
			if isWordChar(runes[index]) {
				index++
				continue
			}
			// A single mid-word character merges between word characters.
			if index+1 < len(runes) && isWordChar(runes[index+1]) &&
				isMidWordChar(runes[index], runes[index-1], runes[index+1]) {
				index += 2
				continue
			}
			break
		}
		segments = append(segments, WordSegment{Segment: string(runes[start:index]), Index: byteOffsets[start], IsWordLike: true})
	}

	return segments
}

func isMidWordChar(mid rune, before rune, after rune) bool {
	switch mid {
	case '.':
		return isWordChar(before) && isWordChar(after)
	case '\'':
		return isLetterRune(before) && isLetterRune(after)
	case ',':
		return isDigitRune(before) && isDigitRune(after)
	}
	return false
}

func isWordChar(r rune) bool {
	return isLetterRune(r) || isDigitRune(r) || isMarkRune(r) || r == '_'
}

// FindWordBackward returns the cursor position after moving one word backward.
func FindWordBackward(text string, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	runes := []rune(text)
	if cursor > len(runes) {
		cursor = len(runes)
	}
	segments := WordSegments(string(runes[:cursor]))

	newCursor := cursor
	// Skip trailing whitespace.
	for len(segments) > 0 && isWhitespaceString(segments[len(segments)-1].Segment) {
		newCursor -= len([]rune(segments[len(segments)-1].Segment))
		segments = segments[:len(segments)-1]
	}
	if len(segments) == 0 {
		return newCursor
	}

	last := segments[len(segments)-1]
	if last.IsWordLike {
		// Skip inside one word-like segment, preserving ASCII punctuation
		// boundaries.
		segment := []rune(last.Segment)
		lastPunctuationEnd := -1
		for index, r := range segment {
			if punctuationChars[r] {
				lastPunctuationEnd = index
			}
		}
		if lastPunctuationEnd < 0 {
			newCursor -= len(segment)
		} else {
			newCursor -= len(segment) - (lastPunctuationEnd + 1)
		}
		return newCursor
	}

	// Skip the non-word non-whitespace run (punctuation).
	for len(segments) > 0 {
		last := segments[len(segments)-1]
		if last.IsWordLike || isWhitespaceString(last.Segment) {
			break
		}
		newCursor -= len([]rune(last.Segment))
		segments = segments[:len(segments)-1]
	}
	return newCursor
}

// FindWordForward returns the cursor position after moving one word forward.
func FindWordForward(text string, cursor int) int {
	runes := []rune(text)
	if cursor >= len(runes) {
		return len(runes)
	}
	segments := WordSegments(string(runes[cursor:]))

	newCursor := cursor
	index := 0
	// Skip leading whitespace.
	for index < len(segments) && isWhitespaceString(segments[index].Segment) {
		newCursor += len([]rune(segments[index].Segment))
		index++
	}
	if index >= len(segments) {
		return newCursor
	}

	next := segments[index]
	if next.IsWordLike {
		// Stop at the first ASCII punctuation inside the word-like segment.
		segment := []rune(next.Segment)
		stop := len(segment)
		for i, r := range segment {
			if punctuationChars[r] {
				stop = i
				break
			}
		}
		newCursor += stop
		return newCursor
	}

	// Skip the non-word non-whitespace run (punctuation).
	for index < len(segments) {
		current := segments[index]
		if current.IsWordLike || isWhitespaceString(current.Segment) {
			break
		}
		newCursor += len([]rune(current.Segment))
		index++
	}
	return newCursor
}

// isWhitespaceString reports whether every rune is whitespace (upstream
// isWhitespaceChar tests a single character; segment lengths are 1 here).
func isWhitespaceString(text string) bool {
	for _, r := range text {
		if !isWhitespaceRune(r) {
			return false
		}
	}
	return len(text) > 0
}
