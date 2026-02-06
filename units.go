package main

// UnitGroup categorizes a set of units (e.g. Volume, Weight, Count).
type UnitGroup struct {
	Label string
	Units []Unit
}

// Unit represents a standard cooking unit.
type Unit struct {
	Value string // stored in DB and form values
	Label string // displayed to user
}

// StandardUnitGroups defines the allowed ingredient units, grouped by category.
var StandardUnitGroups = []UnitGroup{
	{
		Label: "Volume",
		Units: []Unit{
			{Value: "tsp", Label: "tsp"},
			{Value: "tbsp", Label: "tbsp"},
			{Value: "fl oz", Label: "fl oz"},
			{Value: "cup", Label: "cup"},
			{Value: "pt", Label: "pt"},
			{Value: "qt", Label: "qt"},
			{Value: "gal", Label: "gal"},
			{Value: "mL", Label: "mL"},
			{Value: "L", Label: "L"},
		},
	},
	{
		Label: "Weight",
		Units: []Unit{
			{Value: "oz", Label: "oz"},
			{Value: "lb", Label: "lb"},
			{Value: "g", Label: "g"},
			{Value: "kg", Label: "kg"},
		},
	},
	{
		Label: "Count / Other",
		Units: []Unit{
			{Value: "whole", Label: "whole"},
			{Value: "pinch", Label: "pinch"},
			{Value: "dash", Label: "dash"},
			{Value: "clove", Label: "clove"},
			{Value: "bunch", Label: "bunch"},
			{Value: "sprig", Label: "sprig"},
			{Value: "stick", Label: "stick"},
		},
	},
}

// validUnits is the set of allowed unit strings (populated at init time).
var validUnits map[string]bool

func init() {
	validUnits = make(map[string]bool, 20)
	for _, g := range StandardUnitGroups {
		for _, u := range g.Units {
			validUnits[u.Value] = true
		}
	}
}

// IsValidUnit returns true if the unit is a recognized standard unit or empty (unitless).
func IsValidUnit(unit string) bool {
	return unit == "" || validUnits[unit]
}
