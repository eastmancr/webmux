# Webmux Makefile

BINARY := webmux
WM := wm
WM_EMBEDDED := static/wm
VERSION := $(shell grep pkgver= PKGBUILD | cut -d= -f2)

.PHONY: all build dev clean run run-dev check pkg

all: build

# Production build - embeds static/ (including wm binary) into webmux
build: $(WM_EMBEDDED)
	go build -o $(BINARY) .

# Build wm into static/ for embedding
$(WM_EMBEDDED): cmd/wm/main.go
	go build -o $(WM_EMBEDDED) ./cmd/wm

# Dev build - serves static files from disk with live reload
# Also builds wm to project root for convenience during development
dev: $(WM_EMBEDDED)
	go build -tags dev -o $(BINARY) .
	cp $(WM_EMBEDDED) $(WM)

# Clean build artifacts
clean:
	rm -f $(BINARY) $(WM) $(WM_EMBEDDED)

# Run production binary
run: build
	./$(BINARY)

# Run dev binary
run-dev: dev
	./$(BINARY)

# Check that everything compiles
check:
	go build -o /dev/null .
	go build -o /dev/null ./cmd/wm

# Prepare package directory for makepkg
pkg:
	mkdir -p pkg
	tar czf pkg/$(BINARY)-$(VERSION).tar.gz \
		--transform 's,^,$(BINARY)-$(VERSION)/,' \
		main.go dev.go nodev.go go.mod go.sum webmux.1 README.md LICENSE \
		cmd/wm/main.go static/app.js static/index.html static/style.css static/tmux.conf static/favicon.ico
	cp PKGBUILD pkg/
