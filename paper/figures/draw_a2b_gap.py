from pathlib import Path
from html import escape
from reportlab.pdfgen import canvas
from reportlab.pdfbase.pdfmetrics import stringWidth
from figure_style import FONT_NAME
out=Path(__file__).resolve().parent; W,H=518.74,340
c=canvas.Canvas(str(out/'a2b-evidence-gap.pdf'),pagesize=(W,H),initialFontName=FONT_NAME,invariant=1)
c.setFillColor('#ffffff'); c.rect(0,0,W,H,fill=1,stroke=0)
svg=[f'<svg xmlns="http://www.w3.org/2000/svg" width="183mm" height="119.94mm" viewBox="0 0 {W} {H}">','<rect width="518.74" height="340" fill="white"/>']
def line(points,color='#6b7280',dash=False,arrow=False):
 c.setStrokeColor(color); c.setLineWidth(.85); c.setDash(2,3) if dash else c.setDash()
 p=c.beginPath();p.moveTo(points[0][0],H-points[0][1])
 for x,y in points[1:]:p.lineTo(x,H-y)
 c.drawPath(p)
 svg.append('<polyline points="'+' '.join(f'{x},{y}' for x,y in points)+f'" fill="none" stroke="{color}" stroke-width="0.85"'+(' stroke-dasharray="2 3"' if dash else '')+'/>')
 if arrow:
  x,y=points[-1];px,py=points[-2];dx,dy=x-px,y-py;n=(dx*dx+dy*dy)**.5;dx/=n;dy/=n
  a=(x-4*dx+2*dy,y-4*dy-2*dx);b=(x-4*dx-2*dy,y-4*dy+2*dx)
  line([a,(x,y),b],color)
def text(x,y,s,size=10,color='#202a35',center=True):
 c.setFillColor(color);c.setFont(FONT_NAME,size)
 (c.drawCentredString if center else c.drawString)(x,H-y,s)
 svg.append(f'<text x="{x}" y="{y}" font-family="Bitstream Vera Sans,DejaVu Sans,sans-serif" font-size="{size}" fill="{color}" text-anchor="'+('middle' if center else 'start')+'">'+escape(s)+'</text>')
def rect(x,y,w,h,fill='#f4f5f6',stroke='#b4bbc4'):
 c.setFillColor(fill);c.setStrokeColor(stroke);c.setLineWidth(.65);c.setDash();c.rect(x,H-y-h,w,h,fill=1,stroke=1)
 svg.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="{fill}" stroke="{stroke}" stroke-width="0.65"/>')
def box(id,x,y,w,h,label):
 assert stringWidth(label,FONT_NAME,10)<w-10
 svg.append(f'<g id="{id}">');rect(x,y,w,h,'#ffeccc' if id in ('inventory','authority','funds') else '#dae8fc');text(x+w/2,y+h/2+3.5,label);svg.append('</g>')

for x,w,title in [(8,149,'Supplier'),(176,156,'Compliance service'),(351,159,'Buyer')]:
 rect(x,27,w,275,'#ffffff','#cbd0d7');text(x+8,44,title,10,center=False)
box('offer-v1',23,69,119,33,'Offer v1')
box('check',192,69,124,33,'Compliance check')
box('offer-v2',23,155,119,33,'Revised offer v2')
box('acceptance',369,155,123,33,'Acceptance')
box('inventory',23,254,119,33,'Inventory reservation')
box('authority',369,69,123,33,'Delegated authority')
box('funds',369,254,123,33,'Funds reservation')
line([(142,85),(192,85)],arrow=True);text(166,77,'1',10)
line([(82,102),(82,155)],arrow=True);text(94,131,'2',10)
line([(142,171),(369,171)],arrow=True);text(255,161,'3  Exact terms accepted',10)
line([(192,93),(182,93),(182,135),(142,135),(142,155)],'#236d9b',True)
text(254,120,'Do the checks cover',10,'#236d9b');text(254,133,'the revised terms?',10,'#236d9b')
line([(430,102),(430,155)],'#236d9b',True);text(469,131,'Authority?',10,'#236d9b')
line([(430,254),(430,188)],'#236d9b',True);text(468,222,'Funds?',10,'#236d9b')
line([(142,270),(343,270),(343,202),(395,202),(395,188)],'#236d9b',True)
text(251,260,'Inventory reserved?',10,'#236d9b')
line([(14,318),(38,318)],arrow=True);text(44,321,'Scenario sequence / reference',9,center=False)
line([(264,318),(288,318)],'#236d9b',True);text(294,321,'Evidence required before commitment',9,center=False)
svg.append('</svg>');(out/'a2b-evidence-gap.svg').write_text('\n'.join(svg));c.showPage();c.save()
