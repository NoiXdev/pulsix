package checkplugins

// temperaturePlugin: hardware temperature sensors (backlog 3.9). sensors
// optionally limits the check to the listed sensors; the whole-host item
// "" (the sensor count, host-level errors) is reported whatever the list
// says.
func temperaturePlugin() Descriptor {
	return Descriptor{
		Name:                "temperature",
		Runs:                Agent,
		Params:              []ParamSpec{hardwareList("sensors", FormatNoControl)},
		ItemParam:           "sensors",
		HostItemAlwaysAsked: true,
		Metrics: []MetricSpec{
			{Name: "temp.celsius", Kind: Numeric},
			{Name: "temp.high_celsius", Kind: Numeric},
			{Name: "temp.critical_celsius", Kind: Numeric},
			{Name: "temp.sensors", Kind: Numeric},
		},
	}
}
