# tools/modulesign Makefile — builds the Go abi_ver stamper/verifier.
# The root Makefile does not invoke this directly; modulesign is a
# standalone host tool used by scripts/add_abiver.py and the test harness.
#
# Output binary: modulesign (gitignored, lives at tools/modulesign/modulesign).

GO ?= go
GO_TEST_FUZZTIME ?= 30s

.PHONY: modulesign test vet fuzz clean

modulesign:
	$(GO) build -o modulesign .

test: vet
	$(GO) test -v -race -count=1 .

vet:
	$(GO) vet .

# fuzz runs the 30s QA-soak fuzz targets (practice #4).
# Each target soaks independently because go test -fuzz requires exactly
# one match. Override with GO_TEST_FUZZTIME=60s.
fuzz: vet
	$(GO) test -fuzz=FuzzFindAbiVer -fuzztime=$(GO_TEST_FUZZTIME) -run='^$$' -count=1 .
	$(GO) test -fuzz=FuzzReadLEB -fuzztime=$(GO_TEST_FUZZTIME) -run='^$$' -count=1 .

clean:
	rm -f modulesign
