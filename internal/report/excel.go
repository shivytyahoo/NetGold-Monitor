package report

import (
	"fmt"
	"io"

	"github.com/xuri/excelize/v2"
)

// WriteExcel renders the report as an .xlsx workbook with a "Summary"
// sheet (one row per device + a totals row) and an "Outage Details" sheet
// (one row per outage). Durations are human-readable ("2h 14m").
func WriteExcel(rep Report, w io.Writer) error {
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
	totalStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return err
	}
	pctFmt := `0.00"%"`
	pctStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &pctFmt})
	if err != nil {
		return err
	}

	// --- Summary sheet (rename the default sheet) ---
	const sum = "Summary"
	if err := f.SetSheetName("Sheet1", sum); err != nil {
		return err
	}
	sumHeaders := []string{"Device", "Group", "IP", "Outages", "Total Downtime", "Longest Outage", "Uptime %", "Currently"}
	for i, h := range sumHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sum, cell, h)
		f.SetCellStyle(sum, cell, cell, headerStyle)
	}
	row := 2
	for _, d := range rep.Devices {
		vals := []any{d.DeviceName, d.Group, d.IP, d.Outages,
			FormatDuration(d.TotalDowntimeSec), FormatDuration(d.LongestSec), d.UptimePct,
			map[bool]string{true: "Down", false: "Up"}[d.CurrentlyDown]}
		for i, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(i+1, row)
			f.SetCellValue(sum, cell, v)
			if i == 6 {
				f.SetCellStyle(sum, cell, cell, pctStyle)
			}
		}
		row++
	}
	// Totals row
	totVals := []any{"TOTAL (all devices)", "", "", rep.Totals.Outages,
		FormatDuration(rep.Totals.TotalDowntimeSec), FormatDuration(rep.Totals.LongestSec), rep.Totals.UptimePct, ""}
	for i, v := range totVals {
		cell, _ := excelize.CoordinatesToCellName(i+1, row)
		f.SetCellValue(sum, cell, v)
		f.SetCellStyle(sum, cell, cell, totalStyle)
	}
	for _, cw := range []struct {
		col string
		wid float64
	}{{"A", 34}, {"B", 20}, {"C", 16}, {"D", 10}, {"E", 16}, {"F", 16}, {"G", 11}, {"H", 11}} {
		f.SetColWidth(sum, cw.col, cw.col, cw.wid)
	}

	// --- Outage Details sheet ---
	const det = "Outage Details"
	if _, err := f.NewSheet(det); err != nil {
		return err
	}
	detHeaders := []string{"Device", "Group", "IP", "Down Since", "Up At", "Duration", "Status"}
	for i, h := range detHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(det, cell, h)
		f.SetCellStyle(det, cell, cell, headerStyle)
	}
	row = 2
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
		vals := []any{o.DeviceName, o.Group, o.IP, FormatTS(o.DownTS), up, FormatDuration(o.DurationSec), status}
		for i, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(i+1, row)
			f.SetCellValue(det, cell, v)
		}
		row++
	}
	if len(rep.Outages) == 0 {
		f.SetCellValue(det, "A2", "No outages in this period — every device stayed up.")
	}
	for _, cw := range []struct {
		col string
		wid float64
	}{{"A", 34}, {"B", 20}, {"C", 16}, {"D", 18}, {"E", 18}, {"F", 14}, {"G", 30}} {
		f.SetColWidth(det, cw.col, cw.col, cw.wid)
	}

	// Footnote about semantics on the Summary sheet, below the totals row
	// (header row 1 + one row per device + totals row, then a blank row).
	noteCell := fmt.Sprintf("A%d", len(rep.Devices)+4)
	f.SetCellValue(sum, noteCell, "Note: "+SemanticsNote)

	f.SetActiveSheet(0)
	return f.Write(w)
}
