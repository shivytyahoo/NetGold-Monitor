package store

import (
	"time"

	"netgold/internal/devicon"
)

// Topology is a user-built network diagram (v2.0) — deliberately separate
// from the imported legacy maps: topologies are editable, can be
// created by hand, copied from an imported map, or imported from Excel.
type Topology struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Source    string `json:"source"` // manual, excel, map
	CreatedAt int64  `json:"created_at"`
	// Description is a stored note shown with the topology (v3.6);
	// the automatic topologies use it to state where they came from.
	Description string `json:"description,omitempty"`
	Nodes       int    `json:"nodes"` // filled by List/Get
	Links       int    `json:"links"` // filled by List/Get
}

type TopologyNode struct {
	ID         int64   `json:"id"`
	TopologyID int64   `json:"topology_id"`
	Label      string  `json:"label"`
	IP         string  `json:"ip,omitempty"`
	Type       string  `json:"type,omitempty"`
	GroupName  string  `json:"group_name,omitempty"`
	DeviceID   *int64  `json:"device_id,omitempty"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	// Joined at read time from the linked device (by device_id, or by an
	// IP match when device_id is not set):
	DeviceName   string `json:"device_name,omitempty"`
	DeviceStatus string `json:"device_status,omitempty"`
	// The linked device's the legacy system type + icon (v3.1) and the
	// resolved NetGold icon. Node icons prefer the node's own Type,
	// then the linked device's legacy type.
	DeviceType string `json:"device_type,omitempty"`
	TypeIcon   string `json:"type_icon,omitempty"`
	IconID     string `json:"icon_id,omitempty"`
	IconAccent string `json:"icon_accent,omitempty"`
}

type TopologyLink struct {
	ID         int64  `json:"id"`
	TopologyID int64  `json:"topology_id"`
	FromNode   int64  `json:"from_node"`
	ToNode     int64  `json:"to_node"`
	Label      string `json:"label,omitempty"`
	Bandwidth  string `json:"bandwidth,omitempty"`
	// Origin records where the link came from: "" = drawn by hand or
	// from Excel, "imported-map" = reconstructed from an imported legacy
	// map's drawn lines (v3.1).
	Origin string `json:"origin,omitempty"`
}

func (s *Store) CreateTopology(name, source string) (int64, error) {
	if source == "" {
		source = "manual"
	}
	res, err := s.DB.Exec(`INSERT INTO topologies(name, source, created_at) VALUES(?,?,?)`,
		name, source, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListTopologies() ([]Topology, error) {
	rows, err := s.DB.Query(`SELECT t.id, t.name, t.source, t.created_at, IFNULL(t.description,''),
		(SELECT COUNT(*) FROM topology_nodes n WHERE n.topology_id=t.id),
		(SELECT COUNT(*) FROM topology_links l WHERE l.topology_id=t.id)
		FROM topologies t ORDER BY t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topology
	for rows.Next() {
		var t Topology
		if err := rows.Scan(&t.ID, &t.Name, &t.Source, &t.CreatedAt, &t.Description, &t.Nodes, &t.Links); err == nil {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetTopology(id int64) (*Topology, error) {
	var t Topology
	err := s.DB.QueryRow(`SELECT t.id, t.name, t.source, t.created_at, IFNULL(t.description,''),
		(SELECT COUNT(*) FROM topology_nodes n WHERE n.topology_id=t.id),
		(SELECT COUNT(*) FROM topology_links l WHERE l.topology_id=t.id)
		FROM topologies t WHERE t.id=?`, id).Scan(&t.ID, &t.Name, &t.Source, &t.CreatedAt, &t.Description, &t.Nodes, &t.Links)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// SetTopologyDescription stores (or replaces) a topology's
// description text.
func (s *Store) SetTopologyDescription(id int64, description string) error {
	_, err := s.DB.Exec(`UPDATE topologies SET description=? WHERE id=?`, description, id)
	return err
}

func (s *Store) RenameTopology(id int64, name string) error {
	_, err := s.DB.Exec(`UPDATE topologies SET name=? WHERE id=?`, name, id)
	return err
}

func (s *Store) DeleteTopology(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM topology_links WHERE topology_id=?`,
		`DELETE FROM topology_nodes WHERE topology_id=?`,
		`DELETE FROM topologies WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AddTopologyNode(n TopologyNode) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO topology_nodes(topology_id, label, ip, type, group_name, device_id, x, y)
		VALUES(?,?,?,?,?,?,?,?)`, n.TopologyID, n.Label, n.IP, n.Type, n.GroupName, n.DeviceID, n.X, n.Y)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateTopologyNode(n TopologyNode) error {
	_, err := s.DB.Exec(`UPDATE topology_nodes SET label=?, ip=?, type=?, group_name=?, device_id=?, x=?, y=? WHERE id=? AND topology_id=?`,
		n.Label, n.IP, n.Type, n.GroupName, n.DeviceID, n.X, n.Y, n.ID, n.TopologyID)
	return err
}

func (s *Store) DeleteTopologyNode(topologyID, nodeID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM topology_links WHERE topology_id=? AND (from_node=? OR to_node=?)`, topologyID, nodeID, nodeID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM topology_nodes WHERE id=? AND topology_id=?`, nodeID, topologyID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListTopologyNodes(topologyID int64) ([]TopologyNode, error) {
	rows, err := s.DB.Query(`SELECT n.id, n.topology_id, n.label, n.ip, n.type, n.group_name, n.device_id, n.x, n.y,
		IFNULL(d.name,''), IFNULL(d.status,''), IFNULL(d.device_type,''), IFNULL(d.type_icon,''), IFNULL(d.type,'')
		FROM topology_nodes n
		LEFT JOIN devices d ON d.id = n.device_id OR (n.device_id IS NULL AND n.ip <> '' AND d.ip = n.ip)
		WHERE n.topology_id=? ORDER BY n.id`, topologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopologyNode
	for rows.Next() {
		var n TopologyNode
		var pollerType string
		if err := rows.Scan(&n.ID, &n.TopologyID, &n.Label, &n.IP, &n.Type, &n.GroupName, &n.DeviceID, &n.X, &n.Y,
			&n.DeviceName, &n.DeviceStatus, &n.DeviceType, &n.TypeIcon, &pollerType); err == nil {
			// Icon choice: a linked device's stored legacy type/icon is
			// authoritative; otherwise the node's own type text (typed
			// in the editor or from Excel); otherwise the linked
			// device's poller classification.
			var ic devicon.Icon
			switch {
			case n.TypeIcon != "" || n.DeviceType != "":
				ic = devicon.For(n.DeviceType, n.TypeIcon)
			case n.Type != "":
				ic = devicon.ForName(n.Type)
			default:
				ic = devicon.ForName(pollerType)
			}
			n.IconID, n.IconAccent = ic.ID, ic.Accent
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

func (s *Store) GetTopologyNode(topologyID, nodeID int64) (*TopologyNode, error) {
	var n TopologyNode
	err := s.DB.QueryRow(`SELECT id, topology_id, label, ip, type, group_name, device_id, x, y
		FROM topology_nodes WHERE id=? AND topology_id=?`, nodeID, topologyID).
		Scan(&n.ID, &n.TopologyID, &n.Label, &n.IP, &n.Type, &n.GroupName, &n.DeviceID, &n.X, &n.Y)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) AddTopologyLink(l TopologyLink) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO topology_links(topology_id, from_node, to_node, label, bandwidth, origin) VALUES(?,?,?,?,?,?)`,
		l.TopologyID, l.FromNode, l.ToNode, l.Label, l.Bandwidth, l.Origin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) DeleteTopologyLink(topologyID, linkID int64) error {
	_, err := s.DB.Exec(`DELETE FROM topology_links WHERE id=? AND topology_id=?`, linkID, topologyID)
	return err
}

func (s *Store) ListTopologyLinks(topologyID int64) ([]TopologyLink, error) {
	rows, err := s.DB.Query(`SELECT id, topology_id, from_node, to_node, label, bandwidth, IFNULL(origin,'') FROM topology_links WHERE topology_id=? ORDER BY id`, topologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopologyLink
	for rows.Next() {
		var l TopologyLink
		if err := rows.Scan(&l.ID, &l.TopologyID, &l.FromNode, &l.ToNode, &l.Label, &l.Bandwidth, &l.Origin); err == nil {
			out = append(out, l)
		}
	}
	return out, rows.Err()
}
