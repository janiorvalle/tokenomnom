.PHONY: build vet test verify install-hooks

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

# PR 13 adds release-check and installer smoke targets to this CI gate.
verify: build vet test

install-hooks:
	@command -v gitleaks >/dev/null || { echo "gitleaks is required: https://github.com/gitleaks/gitleaks"; exit 1; }
	git config core.hooksPath .githooks
	@echo "Installed the gitleaks pre-commit hook."
