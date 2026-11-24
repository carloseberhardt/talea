# From Conversations to Commitments

A Design Study of Shared Records for Agent-to-Business Coordination

Carlos Eberhardt and Mansura Habiba, IBM Software.

[Read the paper](main.pdf) · [Main LaTeX source](main.tex) · [Reproduce the evidence](evidence/README.md)

This is a design study. The coordination path is a sandbox implementation; commercial gateways, reservation enforcement, restricted channels and the release interface are proposed designs. The supplied checks do not measure comparative coordination benefit or validate a deployed commercial system.

## Contents

- `main.tex`, section `.tex` files, `arxiv.sty`, `references.bib`: manuscript sources.
- `main.pdf`: compiled reading copy.
- `figures/`: six vector PDF figures, editable SVG exports, Python generators, and the supplied ORCID icon. The seventh figure is drawn directly in LaTeX.
- `evidence/`: standard-library Python checks, recorded JSON results and reproduction instructions.
- `build.py`: build, evidence verification, figure generation and arXiv packaging.
- `build/`: ignored compiler logs, intermediates and submission archives.

## Build

Use Python 3.9 or later. For the recommended arXiv-compatible build, install TeX Live with PDFLaTeX and BibTeX (TeX Live 2025 was verified). Required packages include `algorithm2e`, `amsmath`, `amssymb`, `amsthm`, `booktabs`, `caption`, `cleveref`, `enumitem`, `fancyhdr`, `geometry`, `graphicx`, `hyperref`, `iftex`, `listings`, `microtype`, `natbib`, `pgf`/TikZ, `psnfss`, `tabularx` and `xurl`, with their dependencies and standard fonts.

From this directory:

```sh
python3 build.py --engine pdflatex
```

The script uses no shell escape, runs BibTeX, resolves cross-references, checks the final log, and updates `main.pdf`. Tectonic is also supported:

```sh
python3 build.py --engine tectonic
```

The default engine is PDFLaTeX when it and BibTeX are on `PATH`; otherwise the script uses Tectonic. A first Tectonic build may download its standard TeX bundle. The manuscript needs no network access once the TeX packages are installed.

## Verify results

```sh
python3 build.py checks
```

This executes the two checks in a temporary directory and compares their output with the committed JSON results. It does not overwrite the evidence. See [the evidence notes](evidence/README.md) for scope and limitations.

## Regenerate diagrams

The PDF figures are included, so ReportLab is unnecessary for an ordinary paper build. To edit/regenerate the six external figures:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements-figures.txt
.venv/bin/python build.py figures
```

The generators use the Bitstream Vera font distributed with ReportLab and embed it in each PDF. SVG files name that font with a sans-serif fallback. No proprietary font or local font path is required. Rebuild the paper after changing diagrams.

## Prepare arXiv files

```sh
python3 build.py arxiv --engine pdflatex
```

Upload `build/arxiv-source.zip`. Select `main.tex` as the top-level file and `pdflatex` as the compiler. The archive contains the exact section inputs, local style, bibliography source and compiled `.bbl`, six PDF figures, the ORCID icon, and the scripts/results under `anc/`. It excludes `main.pdf`, old drafts, SVG/PNG exports, logs and build tools. Use `build/arxiv-metadata.txt` for the submission form.

The abstract is 1,706 ASCII characters. The verified PDF has 24 pages, seven figures, four tables and 32 references. Recheck these counts if the manuscript changes. The author should review arXiv's generated PDF and select the category and distribution license in the submission form; packaging does not submit or publish anything.

The public project repository is <https://github.com/carloseberhardt/talea/> (public access verified on 24 November 2026). The archived model/check files also stand on their own; the full coordination sandbox is not part of the arXiv ancillary bundle.

Current arXiv guidance: [TeX submissions](https://info.arxiv.org/help/submit_tex.html), [metadata](https://info.arxiv.org/help/prep.html), [ancillary files](https://info.arxiv.org/help/ancillary_files.html).
