package legacyimport

// the legacy system map (diagram) parsing.
//
// A legacy map is two things in the legacy database:
//   - MapView (one row per device group): sDefinition is a <Map> XML doc
//     holding display properties and the map's Envelope bounds (Min/Max X/Y).
//   - Annotation (one row per drawn object): sDefinition is an XML doc whose
//     root element IS the object type (Device, DeviceGroup, Line, Square,
//     Text). Device/DeviceGroup objects carry explicit LocationX/LocationY
//     (the object's centre) and DimensionX/DimensionY (its size); every
//     object carries an Envelope (Primary/Secondary corners). Lines carry
//     StartX/StartY/EndX/EndY. Text objects carry a Label.
//
// These formats were decoded from a real legacy 14.3 production database — 430 of 568 annotations
// carry explicit LocationX/LocationY, the rest position via Envelope.

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

type xmlPoint struct {
	X float64 `xml:"X"`
	Y float64 `xml:"Y"`
}

type xmlEnvelope struct {
	Primary   xmlPoint `xml:"Primary"`
	Secondary xmlPoint `xml:"Secondary"`
}

// annotationXML mirrors the fields any legacy annotation type may carry.
// Unknown elements are ignored by encoding/xml.
type annotationXML struct {
	XMLName    xml.Name
	Object     string      `xml:"Object"`
	Envelope   xmlEnvelope `xml:"Envelope"`
	LocationX  *float64    `xml:"LocationX"`
	LocationY  *float64    `xml:"LocationY"`
	DimensionX *float64    `xml:"DimensionX"`
	DimensionY *float64    `xml:"DimensionY"`
	ZOrder     *int        `xml:"ZOrder"`
	// Line / Square stroke width (the <Width> element every object has).
	Width *float64 `xml:"Width"`
	// Line
	StartX *float64 `xml:"StartX"`
	StartY *float64 `xml:"StartY"`
	EndX   *float64 `xml:"EndX"`
	EndY   *float64 `xml:"EndY"`
	// Square
	Filled  *int `xml:"Filled"`
	Rounded *int `xml:"Rounded"`
	// Text
	Label   string   `xml:"Label"`
	AnchorX *float64 `xml:"AnchorX"`
	AnchorY *float64 `xml:"AnchorY"`
	Font    struct {
		FaceName  string `xml:"FaceName"`
		Height    *int   `xml:"Height"`
		Weight    *int   `xml:"Weight"`
		Italic    *int   `xml:"Italic"`
		Underline *int   `xml:"Underline"`
	} `xml:"Font"`
	// DeviceGroup
	NDeviceGroupID *int64 `xml:"nDeviceGroupID"`
	// appearance
	BaseColor string `xml:"BaseColor"`
	FillColor string `xml:"FillColor"`
	Monitors  struct {
		Monitor []struct {
			Name string `xml:"Name"`
		} `xml:"Monitor"`
	} `xml:"Monitors"`
}

// ParsedAnnotation is the store-ready form of one Annotation row.
type ParsedAnnotation struct {
	Type          string // root element: Device, DeviceGroup, Line, Square, Text
	X, Y          float64
	X2, Y2        float64 // Line end point only
	W, H          float64
	ZOrder        int
	Text          string // Text label
	SourceGroupID int64  // DeviceGroup objects: the legacy group the icon opens
	Extra         string // JSON: colours, monitor names, raw envelope

	// Appearance, decoded for the renderer (v3.1). The same values are
	// mirrored into Extra as JSON so they persist in map_objects.
	LineWidth    float64 // the <Width> element (line stroke / square border)
	BaseColor    string  // raw COLORREF, e.g. "0x00FF0000"
	BaseColorHex string  // converted CSS colour, e.g. "#0000FF"
	FillColor    string  // raw COLORREF
	FillColorHex string
	Filled       bool
	Rounded      bool
	// Text placement + font (Anchor is where legacy pins the label).
	HasAnchor     bool
	AnchorX       float64
	AnchorY       float64
	FontFace      string
	FontSize      int // absolute LOGFONT height (~points at map scale)
	FontWeight    int
	FontBold      bool
	FontItalic    bool
	FontUnderline bool
}

// ParseAnnotation converts one legacy Annotation sDefinition XML document into
// its store-ready form. Positions follow the package convention:
//   - Device/DeviceGroup/Square/Text: X,Y = centre, W,H = size.
//   - Line: X,Y = start, X2,Y2 = end.
func ParseAnnotation(def string) (ParsedAnnotation, error) {
	var a annotationXML
	dec := xml.NewDecoder(strings.NewReader(def))
	if err := dec.Decode(&a); err != nil {
		return ParsedAnnotation{}, fmt.Errorf("annotation xml: %w", err)
	}
	out := ParsedAnnotation{Type: a.XMLName.Local}
	env := a.Envelope
	cx := (env.Primary.X + env.Secondary.X) / 2
	cy := (env.Primary.Y + env.Secondary.Y) / 2
	w := absf(env.Secondary.X - env.Primary.X)
	h := absf(env.Secondary.Y - env.Primary.Y)
	if a.DimensionX != nil {
		w = *a.DimensionX
	}
	if a.DimensionY != nil {
		h = *a.DimensionY
	}
	if a.LocationX != nil {
		cx = *a.LocationX
	}
	if a.LocationY != nil {
		cy = *a.LocationY
	}
	out.X, out.Y, out.W, out.H = cx, cy, w, h
	if a.ZOrder != nil {
		out.ZOrder = *a.ZOrder
	}
	if a.NDeviceGroupID != nil {
		out.SourceGroupID = *a.NDeviceGroupID
	}
	out.Text = strings.TrimSpace(a.Label)

	if out.Type == "Line" {
		// Lines are endpoints, not centre+size.
		out.X, out.Y = env.Primary.X, env.Primary.Y
		out.X2, out.Y2 = env.Secondary.X, env.Secondary.Y
		if a.StartX != nil && a.StartY != nil && a.EndX != nil && a.EndY != nil {
			out.X, out.Y = *a.StartX, *a.StartY
			out.X2, out.Y2 = *a.EndX, *a.EndY
		}
		out.W, out.H = 0, 0
	}

	// Appearance (v3.1): everything the faithful renderer needs.
	if a.Width != nil {
		out.LineWidth = *a.Width
	}
	out.BaseColor = a.BaseColor
	out.FillColor = a.FillColor
	if hex, ok := ColorrefToHex(a.BaseColor); ok {
		out.BaseColorHex = hex
	}
	if hex, ok := ColorrefToHex(a.FillColor); ok {
		out.FillColorHex = hex
	}
	out.Filled = a.Filled != nil && *a.Filled != 0
	out.Rounded = a.Rounded != nil && *a.Rounded != 0
	if a.AnchorX != nil && a.AnchorY != nil {
		out.HasAnchor = true
		out.AnchorX, out.AnchorY = *a.AnchorX, *a.AnchorY
	}
	out.FontFace = strings.TrimSpace(a.Font.FaceName)
	if a.Font.Height != nil {
		out.FontSize = *a.Font.Height
		if out.FontSize < 0 {
			out.FontSize = -out.FontSize
		}
	}
	if a.Font.Weight != nil {
		out.FontWeight = *a.Font.Weight
	}
	out.FontBold = out.FontWeight >= 600
	out.FontItalic = a.Font.Italic != nil && *a.Font.Italic != 0
	out.FontUnderline = a.Font.Underline != nil && *a.Font.Underline != 0

	extra := map[string]any{
		"envelope": []float64{env.Primary.X, env.Primary.Y, env.Secondary.X, env.Secondary.Y},
	}
	if a.BaseColor != "" {
		extra["base_color"] = a.BaseColor
	}
	if out.BaseColorHex != "" {
		extra["base_color_hex"] = out.BaseColorHex
	}
	if a.FillColor != "" {
		extra["fill_color"] = a.FillColor
	}
	if out.FillColorHex != "" {
		extra["fill_color_hex"] = out.FillColorHex
	}
	if out.LineWidth != 0 {
		extra["width"] = out.LineWidth
	}
	if out.Type == "Square" {
		extra["filled"] = out.Filled
		extra["rounded"] = out.Rounded
	}
	if out.HasAnchor {
		extra["anchor"] = []float64{out.AnchorX, out.AnchorY}
	}
	if out.Type == "Text" && (out.FontFace != "" || out.FontSize != 0) {
		extra["font"] = map[string]any{
			"face": out.FontFace, "size": out.FontSize, "weight": out.FontWeight,
			"bold": out.FontBold, "italic": out.FontItalic, "underline": out.FontUnderline,
		}
	}
	if len(a.Monitors.Monitor) > 0 {
		names := make([]string, 0, len(a.Monitors.Monitor))
		for _, m := range a.Monitors.Monitor {
			if m.Name != "" {
				names = append(names, m.Name)
			}
		}
		if len(names) > 0 {
			extra["monitors"] = names
		}
	}
	if b, err := json.Marshal(extra); err == nil {
		out.Extra = string(b)
	}
	return out, nil
}

// mapViewXML mirrors a MapView sDefinition (<Map>) document.
type mapViewXML struct {
	XMLName  xml.Name `xml:"Map"`
	Envelope struct {
		Min xmlPoint `xml:"Min"`
		Max xmlPoint `xml:"Max"`
	} `xml:"Envelope"`
}

// ParsedMapView is the store-ready form of one MapView row's definition.
type ParsedMapView struct {
	MinX, MinY, MaxX, MaxY float64
}

// ParseMapView extracts the map envelope bounds from a MapView sDefinition.
func ParseMapView(def string) (ParsedMapView, error) {
	var m mapViewXML
	if err := xml.NewDecoder(strings.NewReader(def)).Decode(&m); err != nil {
		return ParsedMapView{}, fmt.Errorf("mapview xml: %w", err)
	}
	return ParsedMapView{
		MinX: m.Envelope.Min.X, MinY: m.Envelope.Min.Y,
		MaxX: m.Envelope.Max.X, MaxY: m.Envelope.Max.Y,
	}, nil
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
