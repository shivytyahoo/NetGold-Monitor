//go:build !windows

package legacyimport

// Non-Windows builds have no registry to enumerate; instance discovery
// relies on the SQL Browser (SSRP) probe and the default port instead.
func registryInstances() []string { return nil }

// registryInstancePorts has nothing to read without a Windows registry.
func registryInstancePorts() []InstancePortHint { return nil }
