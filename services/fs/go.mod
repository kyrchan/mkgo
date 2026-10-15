module kernel.lane/services/fs

go 1.24

require (
	kernel.lane/guests/lib v0.0.0
	kernel.lane/kfs v0.0.0
)

replace kernel.lane/guests/lib => ../../guests/lib

replace kernel.lane/kfs => ../../kfs
