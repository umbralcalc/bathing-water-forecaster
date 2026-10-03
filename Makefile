# bathing-water-forecaster — common tasks.
# Run `make` (or `make help`) to list targets. Override knobs inline, e.g.
#   make site WORKERS=4

WORKERS ?= 8          # concurrent site fetches (higher risks EA API throttling)
LIMIT   ?= 0          # cap number of sites (0 = all designated sites)

.DEFAULT_GOAL := help
.PHONY: site site-refresh refs build test fmt vet clean help

site: ## Regenerate the dashboard data (data.js) with the latest data + fitted predictions
	go run ./cmd/export-dashboard -all -workers $(WORKERS) -limit $(LIMIT)
	@echo "→ data.js refreshed; open index.html"

site-refresh: ## Like 'site' but ignore the cache and refetch every site (slow; may rate-limit)
	go run ./cmd/export-dashboard -all -refresh -workers $(WORKERS) -limit $(LIMIT)
	@echo "→ data.js fully refreshed; open index.html"

refs: ## Re-render the References section of index.html from refs/references.bib (needs pandoc)
	@printf -- '---\nnocite: "@*"\n---\n' \
		| pandoc --citeproc --bibliography=refs/references.bib -f markdown -t html > refs/.rendered.html
	@awk 'FNR==NR {r = r $$0 "\n"; next} \
		/<!-- refs:begin/ {print; printf "%s", r; skip=1; next} \
		/<!-- refs:end -->/ {skip=0} !skip' refs/.rendered.html index.html > index.html.tmp
	@mv index.html.tmp index.html && rm -f refs/.rendered.html
	@echo "→ references re-rendered into index.html"

build: ## Compile everything
	go build ./...

test: ## Run all tests
	go test ./...

fmt: ## Format the Go sources
	gofmt -w cmd internal

vet: ## Run go vet
	go vet ./...

clean: ## Drop the per-site fetch cache (forces a full refetch next 'make site')
	rm -rf data/raw/sites

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
