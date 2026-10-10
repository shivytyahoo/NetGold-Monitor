package report

import (
	"fmt"
	"io"
	"time"

	"github.com/jung-kurt/gofpdf"
	"github.com/xuri/excelize/v2"
)

// Status timeline exports (v3.6): the device detail page's status
// timeline — the same segments the page draws — as a downloadable
// Excel workbook or PDF document. Rows are newest first, with the
// columns From, To, State, Duration; the still-open final segment
// shows "now" as its To and is marked as ongoing in the State.

// timelineRows flattens segments into export rows, newest first.
func timelineRows(segs []TimelineSegment) [][4]string {
	rows := make([][4]string, 0, len(segs))
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		to := FormatTS(s.ToTS)
		state := s.State
		if s.Ongoing {
			to = "now"
			state += " (ongoing)"
		}
		rows = append(rows, [4]string{FormatTS(s.FromTS), to, state, FormatDuration(s.DurationSec)})
	}
	return rows
}

// WriteTimelineExcel renders one device's status timeline as an
// .xlsx workbook with a single "Status Timeline" sheet. The title
// row states the device and the exported range (rangeLabel, for
// example "Full history" or "2026-10-03 21:12 to 2026-10-10 21:12").
func WriteTimelineExcel(deviceName, ip string, segs []TimelineSegment, rangeLabel string, w io.Writer) error {
	f := excelize.NewFile()
	defer f.Close()
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"35507A"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	if err != nil {
		return err
	}
	const sheet = "Status Timeline"
	if err := f.SetSheetName("Sheet1", sheet); err != nil {
		return err
	}
	title := fmt.Sprintf("Status timeline — %s (%s) · Range: %s", deviceName, ip, rangeLabel)
	f.SetCellValue(sheet, "A1", title)
	headers := []string{"From", "To", "State", "Duration"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 2)
		f.SetCellValue(sheet, cell, h)
		f.SetCellStyle(sheet, cell, cell, headerStyle)
	}
	row := 3
	for _, r := range timelineRows(segs) {
		for i, v := range r {
			cell, _ := excelize.CoordinatesToCellName(i+1, row)
			f.SetCellValue(sheet, cell, v)
		}
		row++
	}
	for _, cw := range []struct {
		col string
		wid float64
	}{{"A", 18}, {"B", 18}, {"C", 18}, {"D", 14}} {
		f.SetColWidth(sheet, cw.col, cw.col, cw.wid)
	}
	return f.Write(w)
}

// WriteTimelinePDF renders one device's status timeline as a PDF
// document (portrait A4) with the same columns as the Excel export.
// The header line states the device and the exported range.
func WriteTimelinePDF(deviceName, ip string, segs []TimelineSegment, rangeLabel string, w io.Writer) error {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(12, 22, 12)
	pdf.SetAutoPageBreak(true, 16)
	pdf.AliasNbPages("")
	generated := "Generated: " + time.Now().Format("2006-01-02 15:04")
	pdf.SetHeaderFunc(func() {
		pdf.SetY(8)
		pdf.SetFont("Helvetica", "B", 13)
		pdf.CellFormat(0, 7, "NetGold Monitor — Device Status Timeline", "", 1, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(0, 5, fmt.Sprintf("Device: %s (%s)   ·   Range: %s   ·   %s", deviceName, ip, rangeLabel, generated), "", 1, "L", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
		pdf.Line(12, 20, 198, 20)
		pdf.SetY(23)
	})
	pdf.SetFooterFunc(func() {
		pdf.SetY(-13)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 8, fmt.Sprintf("NetGold Monitor — page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})
	pdf.AddPage()
	cols := []struct {
		title string
		w     float64
	}{
		{"From", 42}, {"To", 42}, {"State", 48}, {"Duration", 54},
	}
	headerRow := func() {
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
	headerRow()
	rows := timelineRows(segs)
	if len(rows) == 0 {
		pdf.CellFormat(0, 7, "No state history recorded for this device yet.", "", 1, "L", false, 0, "")
	}
	for _, r := range rows {
		if pdf.GetY() > 272 {
			pdf.AddPage()
			headerRow()
		}
		for i, c := range cols {
			pdf.CellFormat(c.w, 6.5, r[i], "1", 0, "L", false, 0, "")
		}
		pdf.Ln(-1)
	}
	return pdf.Output(w)
}
