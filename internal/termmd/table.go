package termmd

import "strings"

// cells splits a table row at pipes outside code spans and escapes.
func cells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, `\|`) {
		row = row[:len(row)-1]
	}
	var out []string
	var cur strings.Builder
	code := false
	for i := 0; i < len(row); i++ {
		switch c := row[i]; {
		case c == '\\' && i+1 < len(row) && row[i+1] == '|':
			cur.WriteByte('|')
			i++
		case c == '`':
			code = !code
			cur.WriteByte(c)
		case c == '|' && !code:
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, strings.TrimSpace(cur.String()))
}

// table renders a pipe table with aligned columns. When it is wider than
// the width the widest columns shrink and their cells wrap.
func (r *Renderer) table(lines []string) string {
	header := cells(lines[0])
	n := len(header)
	var aligns []byte
	for _, s := range cells(lines[1]) {
		a := byte('l')
		switch {
		case strings.HasPrefix(s, ":") && strings.HasSuffix(s, ":"):
			a = 'c'
		case strings.HasSuffix(s, ":"):
			a = 'r'
		}
		aligns = append(aligns, a)
	}
	for len(aligns) < n {
		aligns = append(aligns, 'l')
	}
	rows := [][]string{header}
	for _, l := range lines[2:] {
		c := cells(l)
		for len(c) < n {
			c = append(c, "")
		}
		rows = append(rows, c[:n])
	}
	spans := make([][][]span, len(rows))
	widths := make([]int, n)
	for i, row := range rows {
		spans[i] = make([][]span, n)
		for j, c := range row {
			base := style(0)
			if i == 0 {
				base = stBold
			}
			spans[i][j] = parseInline(c, base)
			w := 0
			for _, wd := range words(r.decorate(spans[i][j])) {
				if wd.space && w > 0 {
					w++
				}
				w += wd.w
			}
			widths[j] = max(widths[j], w, 1)
		}
	}
	avail := r.opt.Width - 3*(n-1)
	for {
		total, wi := 0, 0
		for j, w := range widths {
			total += w
			if w > widths[wi] {
				wi = j
			}
		}
		if total <= avail || widths[wi] <= 6 {
			break
		}
		widths[wi]--
	}
	bar := r.paint(r.glyph(" │ ", " | "), stDim, "")
	var b strings.Builder
	for i := range rows {
		cellLines := make([][]string, n)
		height := 1
		for j := range n {
			out := r.wrapSpans(spans[i][j], widths[j], "", "")
			cellLines[j] = strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			height = max(height, len(cellLines[j]))
		}
		for k := range height {
			var line strings.Builder
			for j := range n {
				if j > 0 {
					line.WriteString(bar)
				}
				cell := ""
				if k < len(cellLines[j]) {
					cell = cellLines[j][k]
				}
				line.WriteString(align(cell, widths[j], aligns[j]))
			}
			b.WriteString(strings.TrimRight(line.String(), " ") + "\n")
		}
		if i == 0 {
			var parts []string
			for _, w := range widths {
				parts = append(parts, strings.Repeat(r.glyph("─", "-"), w))
			}
			b.WriteString(r.paint(strings.Join(parts, r.glyph("─┼─", "-+-")), stDim, "") + "\n")
		}
	}
	return b.String()
}

func align(cell string, width int, a byte) string {
	gap := max(width-visibleWidth(cell), 0)
	switch a {
	case 'r':
		return strings.Repeat(" ", gap) + cell
	case 'c':
		return strings.Repeat(" ", gap/2) + cell + strings.Repeat(" ", gap-gap/2)
	}
	return cell + strings.Repeat(" ", gap)
}
