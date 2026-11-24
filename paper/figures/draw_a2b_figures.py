from pathlib import Path
from html import escape
from reportlab.pdfgen import canvas
from reportlab.pdfbase.pdfmetrics import stringWidth
from figure_style import FONT_NAME
out=Path(__file__).resolve().parent;W=519
class Figure:
 def __init__(self,name,h):
  self.name=name;self.h=h;self.c=canvas.Canvas(str(out/(name+'.pdf')),pagesize=(W,h),initialFontName=FONT_NAME,invariant=1);self.s=[f'<svg xmlns="http://www.w3.org/2000/svg" width="183mm" height="{h*25.4/72}mm" viewBox="0 0 519 {h}">'];self.rect(0,0,W,h,'white','white')
 def rect(self,x,y,w,h,fill='white',stroke='#81909e'):
  c=self.c;c.setFillColor(fill);c.setStrokeColor(stroke);c.setLineWidth(.65);c.setDash();c.rect(x,self.h-y-h,w,h,fill=1,stroke=1);self.s.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="{fill}" stroke="{stroke}" stroke-width="0.65"/>')
 def text(self,x,y,t,size=10):
  c=self.c;c.setFont(FONT_NAME,size);c.setFillColor('#202a35');c.drawCentredString(x,self.h-y,t);self.s.append(f'<text x="{x}" y="{y}" font-family="Bitstream Vera Sans,DejaVu Sans,sans-serif" font-size="{size}" fill="#202a35" text-anchor="middle">{escape(t)}</text>')
 def box(self,id,x,y,w,h,lines,fill='#ffeccc'):
  self.s.append(f'<g id="{id}">');self.rect(x,y,w,h,fill);top=y+h/2-(len(lines)-1)*6.5+3.3
  size=min(10, min((w-8)*10/stringWidth(t,FONT_NAME,10) for t in lines))
  assert size >= 8.5, (lines,w,size)
  for i,t in enumerate(lines):
   self.text(x+w/2,top+i*13,t,size)
  self.s.append('</g>')
 def line(self,pts,dash=False,arrow=True):
  c=self.c;c.setStrokeColor('#596979');c.setLineWidth(.9);c.setDash(2,3) if dash else c.setDash();p=c.beginPath();p.moveTo(pts[0][0],self.h-pts[0][1])
  for x,y in pts[1:]:p.lineTo(x,self.h-y)
  c.drawPath(p);self.s.append('<polyline points="'+' '.join(f'{x},{y}' for x,y in pts)+'" fill="none" stroke="#596979" stroke-width="0.9"'+(' stroke-dasharray="2 3"' if dash else '')+'/>')
  if arrow:
   x,y=pts[-1];px,py=pts[-2];dx=x-px;dy=y-py;n=(dx*dx+dy*dy)**.5;dx/=n;dy/=n;self.line([(x-4*dx+2*dy,y-4*dy-2*dx),(x,y),(x-4*dx-2*dy,y-4*dy+2*dx)],False,False)
 def save(self):
  self.c.showPage();self.c.save();self.s.append('</svg>');(out/(self.name+'.svg')).write_text('\n'.join(self.s))

# Enlarged vector schematics, 10 pt text at 183 mm width.
f=Figure('talea-coordination',310)
f.box('participant',10,125,100,48,['Participant','CLI / web'],'white')
f.box('pds',146,125,100,48,['PDS','Account records'],'#dae8fc')
f.box('relay',282,125,100,48,['Relay','Record events'],'#dae8fc')
f.box('bridge',409,125,100,48,['Bridge','Dispatch / publish'])
f.box('triage',282,45,100,43,['Triage','Routes requests'])
f.box('appview',409,45,100,43,['AppView','Context / directory'])
f.box('agents',409,236,100,44,['A2A agents','Execute tasks'],'white')
f.line([(110,149),(146,149)]);f.line([(246,149),(282,149)]);f.line([(382,149),(409,149)])
f.line([(332,125),(332,88)]);f.line([(356,125),(356,108),(459,108),(459,88)])
f.line([(489,88),(489,125)])
f.line([(310,45),(310,30),(196,30),(196,125)]);f.text(238,63,'Routing record')
f.line([(440,173),(440,236)]);f.text(418,208,'Task')
f.line([(484,236),(484,173)]);f.text(495,208,'Result')
f.line([(409,158),(397,158),(397,299),(196,299),(196,173)])
f.text(297,290,'Response / sub-request publication',9)
f.save()
f=Figure('talea-a2b-deployment',320)
for x,label,resource in [(10,'Buyer organisation','Budget ledger'),(339,'Seller organisation','Inventory ledger')]:
 f.rect(x,10,170,301);f.text(x+85,27,label,11)
 f.box(label+'-planner',x+10,42,150,43,['Private planner','No signing keys'],'white')
 f.box(label+'-gateway',x+10,112,150,59,['Policy gateway','Release checks',resource+' / outbox'])
 f.box(label+'-deal',x+10,202,150,43,['Deal PDS','Retained evidence'])
 f.box(label+'-public',x+10,269,150,29,['Public profiles'],'#dae8fc')
 f.line([(x+85,85),(x+85,112)]);f.line([(x+85,171),(x+85,202)])
f.box('restricted',205,202,109,43,['Restricted','deal relays'])
f.box('public',205,269,109,29,['Public relays'],'#dae8fc')
f.line([(170,225),(205,225)]);f.line([(349,225),(314,225)])
f.line([(205,213),(190,213),(190,140),(170,140)]);f.line([(314,213),(329,213),(329,140),(349,140)])
f.text(259,170,'Observe and');f.text(259,183,'verify records')
f.line([(170,283),(205,283)]);f.line([(349,283),(314,283)])
f.text(259,258,'Discovery')
f.save()
f=Figure('talea-commitment-evidence',324)
f.box('buyer-authority',183,14,148,43,['Buyer authority','Delegation + receipt'],'#dae8fc')
f.box('seller-authority',361,14,148,43,['Seller authority','Delegation + receipt'],'#dae8fc')
f.box('offer',10,109,130,43,['Offer o','Exact terms'])
f.box('accept',183,109,148,43,['Acceptance a','Exact offer + mandate'])
f.box('confirm',361,109,148,43,['Confirmation c','Exact acceptance'])
f.line([(183,131),(140,131)]);f.text(161,100,'Pins',10)
f.line([(361,131),(331,131)]);f.text(346,100,'Pins',10)
f.line([(257,109),(257,57)]);f.text(293,86,'Authority',10)
f.line([(435,109),(435,57)]);f.text(471,86,'Authority',10)
f.box('budget',183,213,148,43,['Budget ledger','Reserve E(o)'],'white')
f.box('inventory',361,213,148,43,['Inventory ledger','Allocate q(o)'],'white')
for x in [257,435]:
 f.line([(x,152),(x,170)],True,False);f.line([(x,194),(x,213)],True,False);f.text(x,180,'Same local');f.text(x,192,'transaction')
f.box('closure',10,273,499,39,['Alternative terminal outcome: closure', 'No inventory allocation'])
f.save()
f=Figure('booking-reservation-states',270)
f.box('none',10,105,123,45,['Not accepted','No reservation'],'white')
f.box('pending',190,105,143,45,['Awaiting outcome','AED 72,000 reserved'])
f.box('confirmed',380,48,129,45,['Confirmed','AED 72,000','committed'],'#dae8fc')
f.box('closed',380,194,129,45,['Closed','Reservation released'],'white')
f.line([(133,127),(190,127)]);f.text(160,101,'Atomic');f.text(160,114,'acceptance')
f.line([(333,117),(355,117),(355,70),(380,70)]);f.text(366,32,'Verified confirmation')
f.line([(333,138),(355,138),(355,216),(380,216)]);f.text(438,257,'Verified closure')
f.line([(235,105),(235,64),(287,64),(287,105)])
f.text(244,47,'Timeout: retain funds')
f.save()
f=Figure('booking-sequence',570)
xs=[80,259,439]
for x,id,lines in [(80,'agent',['Airline agent','Proposes work']),(259,'buyer',['Airline gateway','Authority / budget']),(439,'seller',['Hotel gateway','Authority / inventory'])]:
 f.box(id,x-70,36,140,43,lines,'white' if x==80 else '#ffeccc');f.line([(x,79),(x,561)],True,False)
def msg(a,b,y,lines):
 f.line([(xs[a],y),(xs[b],y)])
 mid=(xs[a]+xs[b])/2
 for i,t in enumerate(lines):
  width=stringWidth(t,FONT_NAME,10)+8;yy=y-7-(len(lines)-1-i)*13
  f.rect(mid-width/2,yy-10,width,12,'white','white');f.text(mid,yy,t)
def local(x,y,lines):f.box('local-'+str(y),x-79,y,158,43,lines)
msg(0,1,120,['1  Approved work order'])
msg(1,2,158,['2  Lodging request'])
msg(2,1,199,['3  Offer: 120 rooms','at AED 640'])
msg(1,2,240,['4  Approved counter-offer','AED 560'])
msg(2,1,278,['5  Final offer: AED 600'])
local(259,296,['6  Reserve AED 72,000','Acceptance + outbox'])
msg(1,2,373,['7  Acceptance + mandate','Exact offer reference'])
local(439,390,['8  Allocate 120 rooms','Confirmation + outbox'])
msg(2,1,466,['9  Confirmation','Exact acceptance reference'])
local(259,482,['10  Reconcile once','Reserved to committed'])
msg(1,0,555,['11  Confirmed outcome'])
f.save()
