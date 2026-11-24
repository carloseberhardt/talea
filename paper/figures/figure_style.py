"""Embedded font shared by the manuscript's vector diagrams."""
from pathlib import Path

import reportlab
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont

FONT_NAME = "FigureSans"
FONT_FILE = Path(reportlab.__file__).resolve().parent / "fonts" / "Vera.ttf"
pdfmetrics.registerFont(TTFont(FONT_NAME, str(FONT_FILE)))
