//go:build windows

package legacyimport

// Windows: the installed SQL Server instance names live in the registry
// under HKLM\SOFTWARE\Microsoft\Microsoft SQL Server\Instance Names\SQL
// (one value per instance; the value name is the instance name and the
// value data is the instance's service key, e.g. "MSSQL.1"). Each
// service key holds the instance's network configuration at
// <service key>\MSSQLServer\SuperSocketNetLib\Tcp\IPAll, whose TcpPort
// (static) and TcpDynamicPorts (last dynamic port) values say where the
// instance listens — crucial for older installs whose instances use
// dynamic ports while the SQL Browser service is stopped.

import (
	"golang.org/x/sys/windows/registry"

	// Registers go-mssqldb's named-pipe (np:) protocol dialer, which
	// is a separate package in the driver. ConnConfig.PipeName (the
	// LocalDB engine setup) depends on it being registered.
	_ "github.com/microsoft/go-mssqldb/namedpipe"
)

const sqlServerKey = `SOFTWARE\Microsoft\Microsoft SQL Server`

// registryInstanceNames returns the Instance Names\SQL values as a map
// of instance name → service key.
func registryInstanceNames() map[string]string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		sqlServerKey+`\Instance Names\SQL`,
		registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(names))
	for _, n := range names {
		svc, _, err := k.GetStringValue(n)
		if err == nil && svc != "" {
			out[n] = svc
		}
	}
	return out
}

func registryInstances() []string {
	m := registryInstanceNames()
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	return out
}

// registryInstancePorts reads every installed instance's TCP
// configuration (IPAll) from the registry and resolves the ports with
// the pure ParseInstancePorts function. Instances whose IPAll key
// cannot be opened are left out of the value map entirely, so they are
// reported as "no information" rather than "disabled".
func registryInstancePorts() []InstancePortHint {
	names := registryInstanceNames()
	if len(names) == 0 {
		return nil
	}
	ipAll := map[string]map[string]string{}
	for _, svc := range names {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE,
			sqlServerKey+`\`+svc+`\MSSQLServer\SuperSocketNetLib\Tcp\IPAll`,
			registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		vals := map[string]string{}
		if vnames, err := k.ReadValueNames(-1); err == nil {
			for _, vn := range vnames {
				if s, _, err := k.GetStringValue(vn); err == nil {
					vals[vn] = s
				}
			}
		}
		k.Close()
		ipAll[svc] = vals
	}
	return ParseInstancePorts(names, ipAll)
}
