package report

import (
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/jung-kurt/gofpdf"
)

// WritePDF renders the report as a clean multi-page PDF (landscape A4):
// a title block, the per-device summary table, then the outage details
// table, with the semantics footnote at the end. Table header rows are
// repeated on every page and pages are numbered.
func WritePDF(rep Report, w io.Writer) error {
	pdf := gofpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(10, 22, 10)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AliasNbPages("")

	period := fmt.Sprintf("Period: %s to %s", FormatTS(rep.From), FormatTS(rep.To))
	generated := "Generated: " + time.Unix(rep.GeneratedAt, 0).Format("2006-01-02 15:04")
	pdf.SetHeaderFunc(func() {
		pdf.SetY(8)
		pdf.SetFont("Helvetica", "B", 14)
		pdf.CellFormat(0, 7, "NetGold Monitor — Link Downtime Report", "", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(0, 5, period+"   ·   "+generated, "", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
		pdf.Line(10, 20, 287, 20)
		pdf.SetY(23)
	})
	pdf.SetFooterFunc(func() {
		pdf.SetY(-13)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 8, fmt.Sprintf("NetGold Monitor — page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})

	trunc := func(s string, n int) string {
		if utf8.RuneCountInString(s) <= n {
			return s
		}
		r := []rune(s)
		return string(r[:n-1]) + "…"
	}

	headerRow := func(cols []col) {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetFillColor(53, 80, 122)
		pdf.SetTextColor(255, 255, 255)
		for _, c := range cols {
			pdf.CellFormat(c.w, 7, c.title, "1", 0, "C", true, 0, "")
		}
		pdf.Ln(-1)
		pdf.SetTextColor(0, 0, 0)
		pdf.SetFont("Helvetica", "", 9)
	}
	// table draws rows, repeating the header whenever a new page starts.
	table := func(cols []col, rows [][]string) {
		headerRow(cols)
		fill := false
		for _, r := range rows {
			if pdf.GetY() > 188 { // landscape A4 content bottom (~194 with margin)
				pdf.AddPage()
				headerRow(cols)
				fill = false
			}
			if fill {
				pdf.SetFillColor(235, 240, 247)
			} else {
				pdf.SetFillColor(255, 255, 255)
			}
			for i, c := range cols {
				align := "L"
				if c.numeric {
					align = "R"
				}
				pdf.CellFormat(c.w, 6, r[i], "1", 0, align, true, 0, "")
			}
			pdf.Ln(-1)
			fill = !fill
		}
	}

	pdf.AddPage()

	pdf.SetFont("Helvetica", "B", 12)
	pdf.CellFormat(0, 8, "Summary — downtime per device / link", "", 1, "L", false, 0, "")
	sumCols := []col{
		{"Device", 62, false}, {"Group", 38, false}, {"IP", 30, false},
		{"Outages", 18, true}, {"Total Downtime", 34, true}, {"Longest Outage", 34, true},
		{"Uptime %", 22, true}, {"Now", 14, false},
	}
	var sumRows [][]string
	for _, d := range rep.Devices {
		now := "Up"
		if d.CurrentlyDown {
			now = "Down"
		}
		sumRows = append(sumRows, []string{
			trunc(d.DeviceName, 34), trunc(d.Group, 22), d.IP,
			fmt.Sprint(d.Outages), FormatDuration(d.TotalDowntimeSec), FormatDuration(d.LongestSec),
			fmt.Sprintf("%.2f%%", d.UptimePct), now,
		})
	}
	sumRows = append(sumRows, []string{
		"TOTAL (all devices)", "", "",
		fmt.Sprint(rep.Totals.Outages), FormatDuration(rep.Totals.TotalDowntimeSec),
		FormatDuration(rep.Totals.LongestSec), fmt.Sprintf("%.2f%%", rep.Totals.UptimePct), "",
	})
	table(sumCols, sumRows)

	pdf.Ln(6)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.CellFormat(0, 8, "Outage details", "", 1, "L", false, 0, "")
	detCols := []col{
		{"Device", 62, false}, {"Group", 38, false}, {"IP", 30, false},
		{"Down Since", 32, false}, {"Up At", 32, false}, {"Duration", 26, true}, {"Status", 32, false},
	}
	var detRows [][]string
	for _, o := range rep.Outages {
		up := "—"
		status := "Restored"
		if o.Ongoing {
			status = "Ongoing"
		} else if o.UpTS != nil {
			up = FormatTS(*o.UpTS)
		}
		if o.Clipped {
			status += " (began before period)"
		}
		detRows = append(detRows, []string{
			trunc(o.DeviceName, 34), trunc(o.Group, 22), o.IP,
			FormatTS(o.DownTS), up, FormatDuration(o.DurationSec), status,
		})
	}
	if len(detRows) == 0 {
		pdf.SetFont("Helvetica", "", 10)
		pdf.CellFormat(0, 7, "No outages in this period — every device stayed up.", "", 1, "L", false, 0, "")
	} else {
		table(detCols, detRows)
	}

	pdf.Ln(6)
	pdf.SetFont("Helvetica", "I", 8.5)
	pdf.SetTextColor(90, 90, 90)
	pdf.MultiCell(0, 4.5, "Note: "+SemanticsNote, "", "L", false)
	pdf.SetTextColor(0, 0, 0)

	return pdf.Output(w)
}

type col struct {
	title   string
	w       float64
	numeric bool
}
