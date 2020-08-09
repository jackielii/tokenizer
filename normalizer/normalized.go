package normalizer

import (
	// "fmt"
	"log"
	"strings"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/sugarme/tokenizer/util"
)

// RangeType is a enum like representing
// which string (original or normalized) then range
// indexes on.
type IndexOn int

const (
	OriginalTarget = iota
	NormalizedTarget
)

// Range is a slice of indexes on either normalized string or original string
// It is INCLUSIVE start and INCLUSIVE end
type Range struct {
	start   int
	end     int
	indexOn IndexOn
}

func NewRange(start int, end int, indexOn IndexOn) (retVal Range) {
	return Range{
		start:   start,
		end:     end,
		indexOn: indexOn,
	}
}

// NormalizedString keeps both versions of an input string and
// provides methods to access them
type NormalizedString struct {
	Original   string
	Normalized string
	Alignments []Alignment
}

// Alignment maps normalized string to original one using `rune` (Unicode code point)
// Pos: the rune position in the modified (normalized) string and
// Changes: representing the number (size) of inserted/deleted runes from original string
type Alignment struct {
	Pos     int
	Changes int
}

// Normalized is wrapper for a `NormalizedString` and provides
// methods to access it.
type Normalized struct {
	normalizedString NormalizedString
}

// NewNormalizedFrom creates a Normalized instance from string input
func NewNormalizedFrom(s string) *Normalized {
	var alignments []Alignment

	// Break down string to slice of runes
	for i := range []rune(s) {
		alignments = append(alignments, Alignment{
			Pos:     i,
			Changes: i + 1,
		})
	}

	n := NormalizedString{
		Original:   s,
		Normalized: s,
		Alignments: alignments,
	}

	return &Normalized{
		normalizedString: n,
	}

}

func (n *Normalized) Get() NormalizedString {
	return n.normalizedString
}

func (n *Normalized) GetNormalized() string {
	return n.normalizedString.Normalized
}

func (n *Normalized) GetOriginal() string {
	return n.normalizedString.Original
}

// OriginalOffsets returns the range of the original string corresponding to
// the received range on the normalized string.
// Returns None if out of bounds
func (n *Normalized) OriginalOffsets(r []int) []int {
	start := r[0]
	end := len(r) - 1

	var selectedAlignments []Alignment

	firstAlign := n.normalizedString.Alignments[0]
	lastAlign := n.normalizedString.Alignments[len(n.normalizedString.Alignments)-1]

	if start < firstAlign.Pos || end > lastAlign.Changes {
		return nil
	}

	for _, a := range n.normalizedString.Alignments {
		if a.Pos >= start && a.Changes <= end {
			selectedAlignments = append(selectedAlignments, a)
		}
	}

	pos := selectedAlignments[0].Pos
	changes := selectedAlignments[len(selectedAlignments)-1].Changes

	return util.MakeRange(pos, changes)
}

// ConvertOffset converts the given offsets range from referential to the the
// other one (`Original` to `Normalized` and vice versa)
func (n *Normalized) ConvertOffset(r Range) (retVal Range) {

	offset := n.normalizedString.Alignments[0].Changes
	switch r.indexOn {
	case OriginalTarget: // convert to normalized
		start := 0
		end := 0
		// get all alignments in range
		var alignments []Alignment
		for _, a := range n.normalizedString.Alignments {
			if r.end >= a.Changes {
				alignments = append(alignments, a)
			}
		}
		for _, a := range alignments {
			if a.Pos+offset <= r.start {
				start = a.Pos
			}
			if a.Changes <= r.end {
				end = a.Pos
			}
		}
		retVal = Range{
			start:   start,
			end:     end,
			indexOn: NormalizedTarget,
		}
	case NormalizedTarget: // convert to original
		alignments := n.normalizedString.Alignments
		start := alignments[r.start].Pos
		end := alignments[r.end].Pos

		retVal = Range{
			start:   start,
			end:     end,
			indexOn: OriginalTarget,
		}

	default:
		log.Fatalf("Invalid 'indexOn' type: %v\n", r.indexOn)
	}

	return retVal
}

// RangeOf returns a substring of the given string by indexing chars instead of bytes
// It will return empty string if input range is out of bound
func RangeOf(s string, r []int) (retVal string) {
	runes := []rune(s)
	length := len(runes)
	start := r[0]
	end := r[len(r)-1] // inclusive
	// if out of range, return 'empty' string
	if start >= length || end > length || start >= end {
		return ""
	}

	slicedRunes := runes[start:end]
	return string(slicedRunes)
}

// Range returns a substring of the NORMALIZED string (indexing on character not byte)
func (n *Normalized) Range(r Range) (retVal string) {
	var nRange Range

	// Convert to NormalizedRange if r is OriginalRange
	switch r.indexOn {
	case OriginalTarget:
		nRange = n.ConvertOffset(r)
	case NormalizedTarget:
		nRange = r
	default:
		log.Fatalf("Invalid Range type: %v\n", r.indexOn)
	}

	return RangeOf(n.normalizedString.Normalized, util.MakeRange(nRange.start, nRange.end))
}

// RangeOriginal returns substring of ORIGINAL string
func (n *Normalized) RangeOriginal(r Range) string {
	var oRange Range
	switch r.indexOn {
	case NormalizedTarget:
		oRange = n.ConvertOffset(r)
	case OriginalTarget:
		oRange = r
	default:
		log.Fatalf("Invalid Range type: %v\n", r.indexOn)
	}

	rSlice := util.MakeRange(oRange.start, oRange.end)

	return RangeOf(n.normalizedString.Original, rSlice)
}

type ChangeMap struct {
	RuneVal string
	Changes int
}

// Transform applies transformations to the current normalized version, updating the current
// alignments with the new ones.
// This method expect an Iterator yielding each rune of the new normalized string
// with a `change` interger size equals to:
//   - `1` if this is a new rune
//   - `-N` if the char is right before N removed runes
//   - `0` if this rune represents the old one (even if changed)
// Since it is possible that the normalized string doesn't include some of the `characters` (runes) at
// the beginning of the original one, we need an `initial_offset` which represents the number
// of removed runes at the very beginning.
//
// `change` should never be more than `1`. If multiple runes are added, each of
// them has a `change` of `1`, but more doesn't make any sense.
// We treat any value above `1` as `1`.
func (n *Normalized) Transform(m []ChangeMap, initialOffset int) {
	offset := 0
	remainingOffset := initialOffset
	var (
		runeVals  []string
		newAligns []Alignment
	)

	// E.g. string `élégant`
	// Before NFD():  [{233 0} {108 1} {233 2} {103 3} {97 4} {110 5} {116 6}]
	// After NFD(): 	[{101 0} {769 1} {108 2} {101 3} {769 4} {103 5} {97 6} {110 7} {116 8}]
	// New Alignments:
	// {0, 1},
	// {0, 1},
	// {1, 2},
	// {2, 3},
	// {2, 3},
	// {3, 4},
	// {4, 5},
	// {5, 6},
	// {6, 7},

	for i, item := range m {
		var changes int

		if remainingOffset != 0 {
			changes = item.Changes - remainingOffset
			remainingOffset = 0
		} else {
			changes = item.Changes
		}

		// NOTE: offset can be negative or positive value
		// A positive offset means we added `characters` (runes).
		// So we need to remove this offset from the current index to find out the previous id.
		idx := i - offset

		var align Alignment

		switch c := changes; {
		case c > 0: // newly added `character`
			offset += 1 // Or + changes?
			if idx < 1 {
				align = Alignment{
					Pos:     0,
					Changes: 0,
				}
			} else {
				// Get alignment from previous index
				align = n.normalizedString.Alignments[idx-1]
			}

		case c == 0: // no changes
			align = n.normalizedString.Alignments[idx-initialOffset]

		// Some `characters` were removed. We merge our range with one from the
		// removed `characters` as the new alignment
		case c < 0:
			var uch = -changes
			offset += changes
			// aligns := n.normalizedString.Alignments[idx:(idx + uch + 1)]
			aligns := n.normalizedString.Alignments[idx:(idx + uch)]

			// Find max, min from this slice
			// TODO: improve algorithm? gonum?
			var (
				min, max int
				pool     []int
			)
			for _, a := range aligns {
				pool = append(pool, a.Changes)
				pool = append(pool, a.Pos)
			}

			min, max = util.MinMax(pool)

			align = Alignment{
				Pos:     min,
				Changes: max,
			}
		} // end of Switch block

		newAligns = append(newAligns, align)
		runeVals = append(runeVals, item.RuneVal)

	} // end of For-Range block

	n.normalizedString.Alignments = newAligns
	n.normalizedString.Normalized = strings.Join(runeVals, "")

}

func (n *Normalized) NFD() {

	s := n.normalizedString.Normalized
	var (
		changeMap []ChangeMap
		it        norm.Iter
	)
	// Create slice of (char, changes) to map changing
	// if added (inserted) rune, changes = 1; `-N` if char
	// right before N removed chars
	// changes = 0 if this represents the old one (even if changed)

	// Iterating over string and apply tranformer (NFD). One character at a time
	// A `character` is defined as:
	// - a sequence of runes that starts with a starter,
	// - a rune that does not modify or combine backwards with any other rune,
	// - followed by possibly empty sequence of non-starters, that is, runes that do (typically accents).
	// We will iterate over string and apply transformer to each char
	// If a char composes of one rune, there no changes
	// If more than one rune, first is no change, the rest is 1 changes
	it.InitString(norm.NFD, s)
	for !it.Done() {
		runes := []rune(string(it.Next()))

		for i, r := range runes {

			switch i := i; {
			case i == 0:
				changeMap = append(changeMap, ChangeMap{
					// RuneVal: fmt.Sprintf("%+q", r),
					RuneVal: string(r),
					Changes: 0,
				})
			case i > 0:
				changeMap = append(changeMap, ChangeMap{
					// RuneVal: fmt.Sprintf("%+q", r),
					RuneVal: string(r),
					Changes: 1,
				})
			}
		}

	}

	n.Transform(changeMap, 0)
}

func (n *Normalized) NFC() {

	var (
		changeMap []ChangeMap
		it        norm.Iter
	)

	// First, determine which normal form the string is
	s := n.normalizedString.Normalized

	isNFC := norm.Form.IsNormalString(norm.NFC, s)
	// isNFKC := norm.Form.IsNormalString(norm.NFKC, s)
	// isNFD := norm.Form.IsNormalString(norm.NFD, s)
	// isNFKD := norm.Form.IsNormalString(norm.NFKD, s)

	if isNFC {
		return // no need to normalize
	}

	// Assuming the string is in decomposing form
	it.InitString(norm.NFD, s)

	for !it.Done() {
		runes := []rune(string(it.Next()))
		// fmt.Printf("%+q", runes)

		if len(runes) == 1 {
			changeMap = append(changeMap, ChangeMap{
				// RuneVal: fmt.Sprintf("%+q", runes),
				RuneVal: string(runes),
				Changes: 0,
			})
		} else if len(runes) > 1 {
			changeMap = append(changeMap, ChangeMap{
				// RuneVal: fmt.Sprintf("%+q", runes),
				RuneVal: string(runes),
				Changes: -1,
			})
		}
	}

	n.Transform(changeMap, 0)
}

func (n *Normalized) NFKD() {

	s := n.normalizedString.Normalized
	isNFKD := norm.Form.IsNormalString(norm.NFKD, s)
	if isNFKD {
		return // no need to normalize
	}

	var (
		changeMap []ChangeMap
		it        norm.Iter
	)

	it.InitString(norm.NFKD, s)
	for !it.Done() {
		runes := []rune(string(it.Next()))

		for i, r := range runes {

			switch i := i; {
			case i == 0:
				changeMap = append(changeMap, ChangeMap{
					// RuneVal: fmt.Sprintf("%+q", r),
					RuneVal: string(r),
					Changes: 0,
				})
			case i > 0:
				changeMap = append(changeMap, ChangeMap{
					// RuneVal: fmt.Sprintf("%+q", r),
					RuneVal: string(r),
					Changes: 1,
				})
			}
		}

	}

	n.Transform(changeMap, 0)
}

func (n *Normalized) NFKC() {

	var (
		changeMap []ChangeMap
		it        norm.Iter
	)

	// First, determine which normal form the string is
	s := n.normalizedString.Normalized

	isNFKC := norm.Form.IsNormalString(norm.NFKC, s)

	if isNFKC {
		return // no need to normalize
	}

	// Assuming the string is in decomposing form
	it.InitString(norm.NFKD, n.normalizedString.Normalized)

	for !it.Done() {
		runes := []rune(string(it.Next()))

		if len(runes) == 1 {
			changeMap = append(changeMap, ChangeMap{
				// RuneVal: fmt.Sprintf("%+q", runes),
				RuneVal: string(runes),
				Changes: 0,
			})
		} else if len(runes) > 1 {
			changeMap = append(changeMap, ChangeMap{
				// RuneVal: fmt.Sprintf("%+q", runes),
				RuneVal: string(runes),
				Changes: -1,
			})
		}
	}

	n.Transform(changeMap, 0)
}

func (n *Normalized) Filter(fr rune) {

	s := n.normalizedString.Normalized
	var changeMap []ChangeMap

	// Fisrt, reverse the string
	var oRunes []rune

	// Then, iterate over string and apply filtering
	var it norm.Iter
	it.InitString(norm.NFC, s)

	for !it.Done() {
		runes := []rune(string(it.Next()))

		oRunes = append(oRunes, runes...)

	}

	revRunes := make([]rune, 0)
	for i := len(oRunes) - 1; i >= 0; i-- {
		revRunes = append(revRunes, oRunes[i])
	}

	var removed int = 0
	for _, r := range revRunes {
		// fmt.Printf("rune: %+q - filtered rune: %+q\n", r, fr)
		if r == fr {
			removed += 1
		} else {
			if removed > 0 {
				changeMap = append(changeMap, ChangeMap{
					// RuneVal: fmt.Sprintf("%+q", r),
					RuneVal: string(r),
					Changes: -removed,
				})
				removed = 0
			} else if removed == 0 {
				changeMap = append(changeMap, ChangeMap{
					// RuneVal: fmt.Sprintf("%+q", r),
					RuneVal: string(r),
					Changes: 0,
				})
			}
		}
	}

	// Flip back changeMap
	var unrevMap []ChangeMap
	for i := len(changeMap) - 1; i >= 0; i-- {
		unrevMap = append(unrevMap, changeMap[i])
	}

	// fmt.Printf("%v\n", unrevMap)

	n.Transform(unrevMap, removed)
}

func (n *Normalized) RemoveAccents() {

	s := n.normalizedString.Normalized
	b := make([]byte, len(s))

	tf := transform.Chain(transform.RemoveFunc(isMn))

	_, _, err := tf.Transform(b, []byte(s), true)
	if err != nil {
		log.Fatal(err)
	}

	n.normalizedString.Normalized = string(b)
}

// Lowercase transforms string to lowercase
func (n *Normalized) Lowercase() {
	n.normalizedString.Normalized = strings.ToLower(n.normalizedString.Normalized)
}

// Uppercase transforms string to uppercase
func (n *Normalized) Uppercase() {
	n.normalizedString.Normalized = strings.ToUpper(n.normalizedString.Normalized)
}

// SplitOff truncates string with the range [at, len).
// remaining string will contain the range [0, at).
// The provided `at` indexes on `char` not bytes.
func (n *Normalized) SplitOff(at int) {
	if at < 0 {
		log.Fatal("Split off point must be a positive interger number.")
	}
	s := n.normalizedString.Normalized
	if at > len([]rune(s)) {
		n = NewNormalizedFrom("")
	}

	var (
		it       norm.Iter
		runeVals []string
		aligns   []Alignment
	)

	// Split normalized string
	it.InitString(norm.NFC, s)
	for !it.Done() {
		runeVal := string(it.Next())
		runeVals = append(runeVals, runeVal)
	}

	// Alignments
	remainVals := runeVals[0:at]
	for i := range remainVals {
		aligns = append(aligns, Alignment{
			Pos:     i,
			Changes: i + 1,
		})
	}
	n.normalizedString.Normalized = strings.Join(remainVals, "")
	n.normalizedString.Alignments = aligns

	// Split original string
	originalAt := aligns[len(aligns)].Changes // changes of last alignment

	var oRuneVals []string
	it.InitString(norm.NFC, n.normalizedString.Original)
	for !it.Done() {
		runeVal := string(it.Next())
		oRuneVals = append(oRuneVals, runeVal)
	}

	remainORuneVals := oRuneVals[0:originalAt]
	n.normalizedString.Original = strings.Join(remainORuneVals, "")

}

// MergeWith merges an input string with existing one
func (n *Normalized) MergeWith(other NormalizedString) {
	len := n.Len()
	n.normalizedString.Original = strings.Join([]string{n.normalizedString.Original, other.Original}, "")
	n.normalizedString.Normalized = strings.Join([]string{n.normalizedString.Normalized, other.Normalized}, "")

	var ajustedAligns []Alignment
	for _, a := range other.Alignments {
		new := Alignment{
			Pos:     a.Pos + len,
			Changes: a.Changes + len,
		}

		ajustedAligns = append(ajustedAligns, new)
	}

	n.normalizedString.Alignments = append(n.normalizedString.Alignments, ajustedAligns...)

}

// Len returns length (number of runes) of normalized string
func (n *Normalized) Len() int {
	runes := []rune(n.normalizedString.Normalized)
	return len(runes)
}

// LStrip removes leading spaces
func (n *Normalized) LStrip() {
	n.lrstrip(true, false)
}

// RStrip removes trailing spaces
func (n *Normalized) RStrip() {
	n.lrstrip(false, true)
}

// Strip remove leading and trailing spaces
func (n *Normalized) Strip() {
	n.lrstrip(true, true)
}

// lrstrip - Private func to help with exposed strip funcs
func (n *Normalized) lrstrip(left, right bool) {
	var (
		leadingSpaces  int = 0
		trailingSpaces int = 0
		s              string
		changeMap      []ChangeMap
	)

	s = n.normalizedString.Normalized

	runes := []rune(s)

	if left {
		for _, r := range runes {
			if !unicode.IsSpace(r) {
				break
			}

			leadingSpaces += 1
		}
	}

	if right {
		for i := len(runes) - 1; i >= 0; i-- {
			if !unicode.IsSpace(runes[i]) {
				break
			}

			trailingSpaces += 1
		}
	}

	// fmt.Println(runes)
	// fmt.Printf("LeadingSpace: %d\n", leadingSpaces)
	// fmt.Printf("TrailingSpace: %d\n", trailingSpaces)

	if leadingSpaces > 0 || trailingSpaces > 0 {
		for i, r := range runes {
			if i < leadingSpaces || i >= (len(runes)-trailingSpaces) {
				continue
			} else if i == len(runes)-trailingSpaces-1 {
				changeMap = append(changeMap, ChangeMap{
					RuneVal: string(r),
					Changes: -(trailingSpaces),
				})
			} else {
				changeMap = append(changeMap, ChangeMap{
					RuneVal: string(r),
					Changes: 0,
				})
			}
		}

		n.Transform(changeMap, leadingSpaces)
	}

}

func isMn(r rune) bool {
	return unicode.Is(unicode.Mn, r) // Mn: nonspacing marks
}
