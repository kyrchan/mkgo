module kernel.lane/services

go 1.26.0

require (
	kernel.lane/guests/lib v0.0.0
	kernel.lane/services/display v0.0.0
	kernel.lane/services/pkg v0.0.0
)

require golang.org/x/crypto v0.56.0

replace kernel.lane/guests/lib => ../guests/lib

replace kernel.lane/services/display => ./display

replace kernel.lane/services/pkg => ./pkg
