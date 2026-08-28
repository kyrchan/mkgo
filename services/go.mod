module kernel.lane/services

go 1.24

require (
	kernel.lane/guests/lib v0.0.0
	kernel.lane/services/display v0.0.0
)

replace kernel.lane/guests/lib => ../guests/lib

replace kernel.lane/services/display => ./display
