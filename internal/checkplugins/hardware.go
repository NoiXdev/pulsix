package checkplugins

// Bounds of the hardware plugins' lists (backlog 3.9). The agent trusts
// params that pass here, so these and the plugins under
// internal/agent/checks/plugins/ (temperature, smart, raid) agree.
const (
	maxHardwareListEntries = 64
	maxHardwareEntryBytes  = 255
)

// hardwareList is the one optional list each hardware plugin takes.
func hardwareList(name string, format Format) ParamSpec {
	return ParamSpec{
		Name: name, Type: TypeList,
		MaxEntries: maxHardwareListEntries, MaxBytes: maxHardwareEntryBytes, Format: format,
	}
}
