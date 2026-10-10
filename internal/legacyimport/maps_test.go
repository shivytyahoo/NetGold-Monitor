package legacyimport

import (
	"math"
	"strings"
	"testing"
)

// Samples below are trimmed copies of real annotation XML from a
// production legacy 14.3 database (structure and values as stored).

const realDeviceAnn = `<?xml version="1.0"?>
<Device>
	<Object>Device</Object>
	<Envelope>
		<Primary><X>-77.31567383</X><Y>-9.43395996</Y></Primary>
		<Secondary><X>-2.24206543</X><Y>65.63964844</Y></Secondary>
	</Envelope>
	<Width>1</Width>
	<BaseColor>0x00000000</BaseColor>
	<Shadow>0</Shadow>
	<PositionLocked>false</PositionLocked>
	<ZOrder>51</ZOrder>
	<LocationX>-39.77886963</LocationX>
	<LocationY>28.10284424</LocationY>
	<DimensionX>75.07360840</DimensionX>
	<DimensionY>75.07360840</DimensionY>
	<Relayout>false</Relayout>
	<Connections>
	</Connections>
	<Monitors>
		<Monitor>
			<nID>6</nID>
			<Name>Ping</Name>
			<Comment/>
			<Links>
			</Links>
		</Monitor>
	</Monitors>
</Device>`

const realLineAnn = `<?xml version="1.0"?>
<Line>
	<Object>Line</Object>
	<Envelope>
		<Primary><X>124.42376709</X><Y>-98.83410645</Y></Primary>
		<Secondary><X>278.75170898</X><Y>-98.83410645</Y></Secondary>
	</Envelope>
	<Width>3</Width>
	<BaseColor>0x00FF0000</BaseColor>
	<ZOrder>70</ZOrder>
	<StartX>278.75170898</StartX>
	<StartY>-98.83410645</StartY>
	<EndX>124.42376709</EndX>
	<EndY>-98.83410645</EndY>
</Line>`

const realTextAnn = `<?xml version="1.0"?>
<Text>
	<Object>Text</Object>
	<Envelope>
		<Primary><X>-335.64306641</X><Y>-268.01635742</Y></Primary>
		<Secondary><X>-272.35693359</X><Y>-243.98365742</Y></Secondary>
	</Envelope>
	<Width>1</Width>
	<ZOrder>8</ZOrder>
	<Label>CARRIER-A</Label>
	<AnchorX>-335.64306641</AnchorX>
	<AnchorY>-243.98365784</AnchorY>
	<FillColor>0x00FFFFFF</FillColor>
	<Filled>0</Filled>
</Text>`

const realDeviceGroupAnn = `<?xml version="1.0"?>
<DeviceGroup>
	<Object>DeviceGroup</Object>
	<Envelope>
		<Primary><X>-464.00000000</X><Y>-176.00000000</Y></Primary>
		<Secondary><X>-432.00000000</X><Y>-144.00000000</Y></Secondary>
	</Envelope>
	<Width>1</Width>
	<PositionLocked>false</PositionLocked>
	<ZOrder>9999</ZOrder>
	<LocationX>-448.00000000</LocationX>
	<LocationY>-160.00000000</LocationY>
	<DimensionX>32.00000000</DimensionX>
	<DimensionY>32.00000000</DimensionY>
	<Connections>
	</Connections>
	<Monitors>
	</Monitors>
	<nDeviceGroupID>35</nDeviceGroupID>
	<ShortCut>false</ShortCut>
</DeviceGroup>`

const realSquareAnn = `<?xml version="1.0"?>
<Square>
	<Object>Square</Object>
	<Envelope>
		<Primary><X>239.44317627</X><Y>-415.00280762</Y></Primary>
		<Secondary><X>239.44317627</X><Y>-287.00280762</Y></Secondary>
	</Envelope>
	<Width>1</Width>
	<ZOrder>0</ZOrder>
	<FillColor>0x00DCFFFF</FillColor>
	<Filled>1</Filled>
</Square>`

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParseAnnotationDevice(t *testing.T) {
	a, err := ParseAnnotation(realDeviceAnn)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "Device" {
		t.Fatalf("type = %q", a.Type)
	}
	// LocationX/LocationY are the centre and must win over the envelope.
	if !near(a.X, -39.77886963) || !near(a.Y, 28.10284424) {
		t.Fatalf("centre = %v,%v", a.X, a.Y)
	}
	if !near(a.W, 75.07360840) || !near(a.H, 75.07360840) {
		t.Fatalf("size = %v x %v", a.W, a.H)
	}
	if a.ZOrder != 51 {
		t.Fatalf("zorder = %d", a.ZOrder)
	}
	if !strings.Contains(a.Extra, "Ping") {
		t.Fatalf("extra should carry monitor names: %s", a.Extra)
	}
}

func TestParseAnnotationLine(t *testing.T) {
	a, err := ParseAnnotation(realLineAnn)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "Line" {
		t.Fatalf("type = %q", a.Type)
	}
	// Start/End elements are authoritative for lines.
	if !near(a.X, 278.75170898) || !near(a.Y, -98.83410645) || !near(a.X2, 124.42376709) || !near(a.Y2, -98.83410645) {
		t.Fatalf("line = (%v,%v) -> (%v,%v)", a.X, a.Y, a.X2, a.Y2)
	}
}

func TestParseAnnotationText(t *testing.T) {
	a, err := ParseAnnotation(realTextAnn)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "Text" || a.Text != "CARRIER-A" {
		t.Fatalf("text = %+v", a)
	}
	// No LocationX on Text: centre falls back to envelope midpoint.
	if !near(a.X, (-335.64306641+-272.35693359)/2) || !near(a.Y, (-268.01635742+-243.98365742)/2) {
		t.Fatalf("centre = %v,%v", a.X, a.Y)
	}
}

func TestParseAnnotationDeviceGroup(t *testing.T) {
	a, err := ParseAnnotation(realDeviceGroupAnn)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "DeviceGroup" || a.SourceGroupID != 35 {
		t.Fatalf("group ann = %+v", a)
	}
	if !near(a.X, -448) || !near(a.Y, -160) || !near(a.W, 32) || !near(a.H, 32) {
		t.Fatalf("group pos/size = %v,%v %vx%v", a.X, a.Y, a.W, a.H)
	}
}

func TestParseAnnotationSquareEnvelopeFallback(t *testing.T) {
	a, err := ParseAnnotation(realSquareAnn)
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "Square" {
		t.Fatalf("type = %q", a.Type)
	}
	// No Location/Dimension: centre+size derived from envelope.
	if !near(a.X, 239.44317627) || !near(a.Y, (-415.00280762+-287.00280762)/2) {
		t.Fatalf("centre = %v,%v", a.X, a.Y)
	}
	if !near(a.W, 0) || !near(a.H, 128) {
		t.Fatalf("size = %v x %v", a.W, a.H)
	}
}

func TestParseMapView(t *testing.T) {
	const def = `<?xml version="1.0"?>
<Map>
	<Properties><ShowGrid>true</ShowGrid><GridDivisions>16.00000000</GridDivisions></Properties>
	<Envelope>
		<Min><X>-514.27636719</X><Y>-304.00000000</Y></Min>
		<Max><X>465.37890625</X><Y>264.00000000</Y></Max>
	</Envelope>
</Map>`
	m, err := ParseMapView(def)
	if err != nil {
		t.Fatal(err)
	}
	if !near(m.MinX, -514.27636719) || !near(m.MinY, -304) || !near(m.MaxX, 465.37890625) || !near(m.MaxY, 264) {
		t.Fatalf("bounds = %+v", m)
	}
}

func TestParseAnnotationBadXML(t *testing.T) {
	if _, err := ParseAnnotation("<Device><Envelope>"); err == nil {
		t.Fatal("expected error for truncated xml")
	}
}
