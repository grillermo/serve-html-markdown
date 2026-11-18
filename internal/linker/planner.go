package linker

import "sort"

const crossBlockMessage = "Selection spans multiple paragraphs — select within one."

// Plan ports Planner.plan: it turns a plain-text match into the source ranges
// to wrap in links, or an UnsafeMatch error.
func Plan(m *Map, plain Range, source string) ([]Range, error) {
	if err := rejectCrossBlock(m.BlockIDs[plain.Begin:plain.End]); err != nil {
		return nil, err
	}
	r := m.SourceRange(plain)
	if err := rejectUnlinkable(m, r); err != nil {
		return nil, err
	}
	r = snap(m, r)
	lo, hi := m.PlainIndicesWithin(r)
	if err := rejectCrossBlock(m.BlockIDs[lo:hi]); err != nil {
		return nil, err
	}

	var segments []Range
	for _, seg := range segment(m, r) {
		if !meaningful(m, seg) {
			continue
		}
		if t, ok := trim(seg, source); ok {
			segments = append(segments, t)
		}
	}
	if len(segments) == 0 {
		return nil, unsafeMatch("Selection overlaps an existing link.")
	}
	return segments, nil
}

// rejectCrossBlock ports reject_cross_block!: more than one distinct id.
func rejectCrossBlock(ids []int32) error {
	for _, id := range ids {
		if id != ids[0] {
			return unsafeMatch(crossBlockMessage)
		}
	}
	return nil
}

// rejectUnlinkable ports reject_unlinkable!.
func rejectUnlinkable(m *Map, r Range) error {
	for _, zone := range m.UnlinkableRanges {
		if zone.Begin >= r.End || zone.End <= r.Begin {
			continue
		}
		return unsafeMatch("Selection is inside a code block.")
	}
	return nil
}

// snap ports Planner.snap: extend the range until no inline element is
// partially overlapped: an element with exactly one of its tags inside the
// range is pulled in whole. Elements fully inside or fully containing the
// range are fine. Like the Ruby, the range is updated while iterating.
func snap(m *Map, r Range) Range {
	for {
		changed := false
		for _, el := range m.InlineRanges {
			if el.Begin >= r.End || el.End <= r.Begin {
				continue
			}
			if el.Begin >= r.Begin && el.End <= r.End {
				continue
			}
			if el.Begin < r.Begin && el.End > r.End {
				continue
			}
			r = Range{min(r.Begin, el.Begin), max(r.End, el.End)}
			changed = true
		}
		if !changed {
			return r
		}
	}
}

// segment ports Planner.segment: the range minus every existing link range, as
// the maximal pieces in source order.
//
// The Ruby subtracts each link (sorted by begin) from a list of segments in
// turn. Link ranges are never empty, so every subtraction leaves a gap between
// its pieces and the result is exactly the set difference, which one sweep
// over the begin-sorted links computes. Links that do not overlap the range
// cannot affect it and are skipped.
func segment(m *Map, r Range) []Range {
	links := make([]Range, 0, 8)
	for _, l := range m.LinkRanges {
		if l.Begin < r.End && l.End > r.Begin {
			links = append(links, l)
		}
	}
	sort.SliceStable(links, func(i, j int) bool { return links[i].Begin < links[j].Begin })

	var out []Range
	cursor := r.Begin
	for _, l := range links {
		if l.Begin > cursor {
			out = append(out, Range{cursor, l.Begin})
		}
		cursor = max(cursor, l.End)
	}
	if cursor < r.End {
		out = append(out, Range{cursor, r.End})
	}
	return out
}

// meaningful ports Planner.meaningful?: the segment covers at least one
// non-whitespace plain byte.
func meaningful(m *Map, seg Range) bool {
	lo, hi := m.PlainIndicesWithin(seg)
	for _, b := range m.Plain[lo:hi] {
		if !isSpace(b) {
			return true
		}
	}
	return false
}

// trim ports Planner.trim: strip source whitespace from both segment edges.
func trim(seg Range, source string) (Range, bool) {
	from, upto := seg.Begin, seg.End
	for from < upto && isSpace(source[from]) {
		from++
	}
	for upto > from && isSpace(source[upto-1]) {
		upto--
	}
	if from == upto {
		return Range{}, false
	}
	return Range{from, upto}, true
}
