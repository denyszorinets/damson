"""Sphinx configuration for the Damson documentation site (ADR-0001)."""

project = "Damson"
author = "The Damson authors"
copyright = "2026, The Damson authors. Licensed under GPL-3.0-or-later"

# Repository URL (ADR-0023). Change it here only.
repo_url = "https://github.com/denyszorinets/damson"

extensions = [
    "myst_parser",  # only to include the root Markdown files (ARCHITECTURE.md)
    "sphinx.ext.extlinks",
    "sphinx_design",
    "sphinxcontrib.mermaid",
]

source_suffix = {".rst": "restructuredtext"}
root_doc = "index"
exclude_patterns = ["_build", ".venv", "README.md", "superpowers", "SCAFFOLDING.md"]

extlinks = {
    "repo": (f"{repo_url}/blob/main/%s", "%s"),
    "issue": (f"{repo_url}/issues/%s", "#%s"),
}

myst_heading_anchors = 3
myst_enable_extensions = ["colon_fence"]
suppress_warnings = ["myst.header"]

html_theme = "furo"
html_title = "Damson"
html_static_path = []

nitpicky = True

# The repository is not publicly reachable yet (404 on 2026-10-27), so its
# links cannot be checked. Remove this once it is public.
linkcheck_ignore = [rf"{repo_url}.*"]
linkcheck_anchors_ignore_for_url = [r"https://github\.com/.*"]


def setup(app):
    # Damson's SQL examples use Postgres placeholders ($1), which the generic
    # SQL lexer rejects.
    from pygments.lexers.sql import PostgresLexer

    app.add_lexer("sql", PostgresLexer)
