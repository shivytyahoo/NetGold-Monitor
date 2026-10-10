package legacyimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures in testdata/ are verbatim Annotation documents from a
// real the legacy system 14.3 production database (see the map design
// evidence in the project records).

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestColorrefToHex(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"0x00FF0000", "#0000FF", true}, // legacy link blue
		{"0x00DCFFFF", "#FFFFDC", true}, // pale yellow region fill
		{"0x00808000", "#008080", true}, // teal region fill
		{"0x00000000", "#000000", true},
		{"0x00FFFFFF", "#FFFFFF", true},
		{"0x000000FF", "#FF0000", true}, // low byte is red
		{"", "", false},
		{"blue", "", false},
		{"0xZZZ", "", false},
	}
	for _, c := range cases {
		got, ok := ColorrefToHex(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ColorrefToHex(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseRealLineFixture(t *testing.T) {
	pa, err := ParseAnnotation(fixture(t, "sample-line.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Type != "Line" {
		t.Fatalf("type = %q", pa.Type)
	}
	// Start/End elements win over the envelope (and are swapped in it).
	if pa.X != 278.75170898 || pa.Y != -98.83410645 || pa.X2 != 124.42376709 || pa.Y2 != -98.83410645 {
		t.Errorf("endpoints = (%v,%v)-(%v,%v)", pa.X, pa.Y, pa.X2, pa.Y2)
	}
	if pa.LineWidth != 3 {
		t.Errorf("width = %v, want 3", pa.LineWidth)
	}
	if pa.BaseColorHex != "#0000FF" {
		t.Errorf("base colour = %q, want #0000FF (COLORREF blue)", pa.BaseColorHex)
	}
	if pa.ZOrder != 70 {
		t.Errorf("zorder = %d, want 70", pa.ZOrder)
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(pa.Extra), &extra); err != nil {
		t.Fatal(err)
	}
	if extra["width"] != 3.0 || extra["base_color_hex"] != "#0000FF" {
		t.Errorf("extra = %v", extra)
	}
}

func TestParseRealSquareFixture(t *testing.T) {
	pa, err := ParseAnnotation(fixture(t, "sample-square.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Type != "Square" {
		t.Fatalf("type = %q", pa.Type)
	}
	if !pa.Filled {
		t.Error("square should be Filled")
	}
	if pa.Rounded {
		t.Error("square should not be Rounded")
	}
	if pa.FillColorHex != "#FFFFDC" {
		t.Errorf("fill = %q, want #FFFFDC (pale yellow)", pa.FillColorHex)
	}
	// The envelope is a vertical strip: X constant, height 128.
	if pa.H != 128 {
		t.Errorf("height = %v, want 128", pa.H)
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(pa.Extra), &extra); err != nil {
		t.Fatal(err)
	}
	if extra["filled"] != true || extra["fill_color_hex"] != "#FFFFDC" {
		t.Errorf("extra = %v", extra)
	}
}

func TestParseRealTextFixture(t *testing.T) {
	pa, err := ParseAnnotation(fixture(t, "sample-text.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Type != "Text" {
		t.Fatalf("type = %q", pa.Type)
	}
	if pa.Text != "CARRIER-A" {
		t.Errorf("label = %q, want CARRIER-A", pa.Text)
	}
	if !pa.HasAnchor || pa.AnchorX != -335.64306641 || pa.AnchorY != -243.98365784 {
		t.Errorf("anchor = (%v,%v) has=%v", pa.AnchorX, pa.AnchorY, pa.HasAnchor)
	}
	if pa.FontFace != "Arial" || pa.FontSize != 21 || pa.FontWeight != 700 {
		t.Errorf("font = %q %dpt w%d", pa.FontFace, pa.FontSize, pa.FontWeight)
	}
	if !pa.FontBold || !pa.FontUnderline || pa.FontItalic {
		t.Errorf("font flags bold=%v underline=%v italic=%v", pa.FontBold, pa.FontUnderline, pa.FontItalic)
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(pa.Extra), &extra); err != nil {
		t.Fatal(err)
	}
	font, _ := extra["font"].(map[string]any)
	if font == nil || font["face"] != "Arial" || font["underline"] != true {
		t.Errorf("extra font = %v", extra["font"])
	}
	anchor, _ := extra["anchor"].([]any)
	if len(anchor) != 2 {
		t.Errorf("extra anchor = %v", extra["anchor"])
	}
}

func TestParseRealDeviceFixture(t *testing.T) {
	pa, err := ParseAnnotation(fixture(t, "sample-device.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if pa.Type != "Device" {
		t.Fatalf("type = %q", pa.Type)
	}
	// LocationX/LocationY is the icon CENTRE.
	if pa.X != -39.77886963 || pa.Y != 28.10284424 {
		t.Errorf("centre = (%v,%v)", pa.X, pa.Y)
	}
	if pa.W < 75 || pa.W > 76 {
		t.Errorf("dimension = %v, want ~75", pa.W)
	}
}
