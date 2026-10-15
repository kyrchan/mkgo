# tools/img Makefile -- builds the Go disk image builder.
# The root Makefile invokes this via `$(MAKE) -C tools/img img`.
#
# Output binary: img (gitignored, lives at tools/img/img).

GO ?= go
GO_TEST_FUZZTIME ?= 30s

.PHONY: img test vet fuzz clean

img:
	$(GO) build -o img .

test: vet
	$(GO) test -v -race -count=1 .

vet:
	$(GO) vet .

# fuzz runs the 30s QA-soak fuzz targets (practice #4).
# Use GO_TEST_FUZZTIME to override (e.g. GO_TEST_FUZZTIME=60s make fuzz).
fuzz: vet
	$(GO) test -fuzz=. -fuzztime=$(GO_TEST_FUZZTIME) -run='^$$' -count=1 .

clean:
	rm -f img
