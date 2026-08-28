module kernel.lane/services/console

go 1.24

require (
	kernel.lane/guests/lib v0.0.0
	kernel.lane/services/display v0.0.0-00010101000000-000000000000
)

replace kernel.lane/guests/lib => ../../guests/lib

replace kernel.lane/services/display => ../display
