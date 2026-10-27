# Damson Makefile. M0 adds build, check, gen, test and dev targets (see CLAUDE.md).

DOCS_DIR := docs
SPHINX   := cd $(DOCS_DIR) && uv run --frozen

.PHONY: docs docs-serve docs-check

docs: ## Build the documentation site into docs/_build/html
	$(SPHINX) sphinx-build -b html . _build/html

docs-serve: ## Live-reloading docs preview on http://127.0.0.1:8000
	$(SPHINX) sphinx-autobuild --host 127.0.0.1 --port 8000 . _build/html

docs-check: ## Strict docs build (warnings are errors) plus linkcheck
	$(SPHINX) sphinx-build -W --keep-going -n -b html . _build/html
	$(SPHINX) sphinx-build -W --keep-going -b linkcheck . _build/linkcheck
