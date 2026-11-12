package checkplugins

// servicePlugin: the state of each listed service unit. There is no "every
// unit" default, so units is required and must name at least one.
func servicePlugin() Descriptor {
	return Descriptor{
		Name: "service",
		Runs: Agent,
		Params: []ParamSpec{
			{Name: "units", Type: TypeList, Required: true, NonEmpty: true, Format: FormatNotBlank},
		},
		ItemParam: "units",
		Metrics: []MetricSpec{
			{Name: "service.state", Kind: Discrete},
		},
	}
}
