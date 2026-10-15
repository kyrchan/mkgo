module kernel.lane/services/ssh

go 1.24

require (
	golang.org/x/crypto v0.26.0
	kernel.lane/guests/lib v0.0.0
)

require golang.org/x/sys v0.23.0 // indirect

replace kernel.lane/guests/lib => ../../guests/lib
