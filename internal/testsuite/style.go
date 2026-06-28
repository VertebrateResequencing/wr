/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

package testsuite

const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiCyan   = "\x1b[36m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiReset  = "\x1b[0m"
)

// style colours and decorates summary and progress text. When rich is set (the
// target stream is a real terminal) its methods wrap text in ANSI escapes and
// its glyph accessors return Unicode; otherwise text is returned unchanged and
// glyphs fall back to ASCII, so piped or logged output stays plain and
// greppable.
type style struct {
	rich bool
}

// newStyle returns a style that emits ANSI colour and Unicode glyphs when rich
// is true (its target stream is a terminal) and plain ASCII otherwise.
func newStyle(rich bool) style {
	return style{rich: rich}
}

func (s style) wrap(code string, text string) string {
	if !s.rich {
		return text
	}

	return code + text + ansiReset
}

func (s style) green(text string) string  { return s.wrap(ansiGreen, text) }
func (s style) yellow(text string) string { return s.wrap(ansiYellow, text) }
func (s style) red(text string) string    { return s.wrap(ansiRed, text) }
func (s style) cyan(text string) string   { return s.wrap(ansiCyan, text) }
func (s style) bold(text string) string   { return s.wrap(ansiBold, text) }
func (s style) dim(text string) string    { return s.wrap(ansiDim, text) }

// boldGreen and boldRed combine weight with colour for the final PASSED/FAILED
// markers without emitting a redundant reset between the two codes.
func (s style) boldGreen(text string) string { return s.wrap(ansiBold+ansiGreen, text) }
func (s style) boldRed(text string) string   { return s.wrap(ansiBold+ansiRed, text) }

func (s style) glyph(rich string, plain string) string {
	if s.rich {
		return rich
	}

	return plain
}

// pass is the all-passed package marker.
func (s style) pass() string { return s.glyph("✓", "") }

// skip is the marker for a package that also has skips.
func (s style) skip() string { return s.glyph("◦", "") }

// skipArrow precedes each skip description.
func (s style) skipArrow() string { return s.glyph("↳", "-") }

// bullet separates fields on the grand-total and progress lines.
func (s style) bullet() string { return s.glyph("·", "·") }

// rule is the character repeated to draw the horizontal rule.
func (s style) rule() string { return s.glyph("─", "-") }

// fail is the failed-suite marker.
func (s style) fail() string { return s.glyph("✗", "") }

// times is the multiplier shown by deduplicated skip descriptions.
func (s style) times() string { return s.glyph("×", "x") }
