# Animation plan — Remotion, tagless weather query

A plan for a ~45 second animated trace of one signal through the Murmur stack,
rendered with Remotion as an MP4 we can drop into a slide, embed in the writeup,
or share standalone.

## Goal

Show a friend who has not seen the system before what happens, end-to-end, when
Carlos posts an untagged question and a specialist agent answers it. The viewer
should walk away with three things lodged:

1. The firehose is the bus — every component is either writing to PDS or
   reading from the relay.
2. Triage is the LLM that picks the agent, not a pass-through. Domain agents
   are pure A2A receivers.
3. The Bridge is the only thing that talks A2A. Everything else is signed
   records on the wire.

If a viewer can finish the animation and re-tell the trace in their own words,
it worked. Anything fancier than that is gravy.

## Scenario

Carlos posts: **"what's the weather in Austin?"** — tagless, via the Web UI
(the CLI is operator tooling, not the user surface).

Expected flow, hop by hop:

| # | Actor | Action | Surface |
|---|---|---|---|
| 1 | Carlos | types question in browser | Web UI |
| 2 | Web UI → AppView | `POST /api/post` | HTTP |
| 3 | AppView (Poster) | `createRecord` signed as carlos | → Caddy → PDS (SQLite) |
| 4 | PDS → Relay | firehose commit broadcast | WSS firehose |
| 5 | Triage | sees tagless request, calls LLM via AgentGateway | LLM proxy → WatsonX |
| 6 | Triage | publishes routing signal naming `weather-one.test` | → Caddy → PDS → relay |
| 7 | Bridge | sees routing signal, dispatches A2A SendMessage | green A2A hop |
| 8 | Weather agent | calls LLM, returns answer over A2A | A2A response |
| 9 | Bridge | writes result signal signed as weather-one | → Caddy → PDS → relay |
| 10 | AppView | indexes result, pushes to Web UI via WS | browser updates |

**A note on storage that's worth holding onto for the captions.** The PDS uses
**SQLite** (one database per account, on disk) for the actual repo — records,
the Merkle tree, signing keys. **Postgres** in this stack is the PLC's
database — DID documents and their rotation history, not records. Carlos's
posts never touch Postgres. The "DID lookup" arrow on the architecture
diagram is the only thing tying PDS and Postgres together, and it's a read
against PLC, not a write of records. Phone book vs. mailbox.

## Visual language

- **Reuse the existing diagram** (`architecture.drawio`) as the spatial layout.
  Export each box as an SVG primitive in JSX rather than rasterizing — we want
  per-element opacity, glow, and pulse control.
- **Color rules already established by the diagram's legend:**
  - blue = record write (HTTPS to PDS)
  - orange dashed = firehose subscription
  - green = A2A dispatch
  - purple = LLM inference
  - gray dashed = internal HTTP API (AppView read/post)
- **Motion vocabulary** (keep narrow):
  - "Box at rest" = 50% opacity, gray-tinted
  - "Box activates" = full opacity + 8px outer glow in the box's category color,
    held for the duration that box is doing work
  - "Edge fires" = a colored dot travels along the path source → target over
    ~12 frames, then the edge stays lit at 30% opacity for ~30 frames as a
    persistence trail, then fades
  - "LLM call" = a small spinner near the calling box, ~30 frames
- **Captions, not voiceover.** A single line of caption at the bottom for each
  beat, fading in/out with the activity. Voiceover is a later upgrade and adds
  a recording step we don't need for v1.

The discipline: every motion answers "what just happened?" Nothing decorative.

## Timing

30 fps. 45 seconds = 1350 frames. Budget per beat:

| Beat | Frames | Seconds |
|---|---|---|
| Diagram intro (fade in, settle) | 0–90 | 0:00–0:03 |
| Carlos types the question | 90–180 | 0:03–0:06 |
| Write to PDS (CLI → Caddy → PDS) | 180–270 | 0:06–0:09 |
| PDS → Relay (firehose commit) | 270–360 | 0:09–0:12 |
| Relay → 3 subscribers light up | 360–450 | 0:12–0:15 |
| Triage thinks (LLM call) | 450–600 | 0:15–0:20 |
| Triage publishes routing signal | 600–750 | 0:20–0:25 |
| Bridge dispatches A2A to weather | 750–900 | 0:25–0:30 |
| Weather agent thinks (LLM call) | 900–1050 | 0:30–0:35 |
| Bridge writes result, fans back through relay | 1050–1200 | 0:35–0:40 |
| Browser shows the answer | 1200–1350 | 0:40–0:45 |

If a beat needs more breathing room when we watch it, we extend — the total
duration is `durationInFrames` on the Composition, easy to bump.

## Captions

One caption at a time, bottom of frame, fades in ~10 frames after its beat
starts and fades out ~10 frames before the next caption appears. Never two on
screen at once. Each one earns its three to five seconds — if it doesn't, cut
it.

Style: present tense, short, declarative, no hedging. The narrator is the
diagram, not a salesperson.

**Pedagogical goal:** assume the audience has not seen atproto vocabulary
before. Use the captions to introduce, in order, the key terms — **DID**,
**record**, **repo**, **PDS**, **commit**, **firehose**, **A2A** — naming them
the first time they appear so the visual and the term land together. Bold
marks first-use in the table below; in the rendered caption it can be a tiny
underline, a brief color highlight, or just left as plain prose — the table
is for us to verify each term gets introduced exactly once.

| Beat | Frames | Caption |
|---|---|---|
| Diagram intro | 0–90 | *(title card overlay)* What happens when Carlos asks: "what's the weather in Austin?" |
| Carlos types | 90–180 | Carlos posts via the Web UI — no @-mention. |
| Write to PDS | 180–270 | His post becomes a signed **record** in his **repo** on the **PDS**. |
| PDS → Relay | 270–360 | Each new record is a **commit**. The relay broadcasts every commit on a **firehose**. |
| Relay fanout | 360–450 | Every Murmur service subscribes to the same firehose. |
| Triage LLM | 450–600 | Triage reads it off the firehose and asks an LLM: who should answer? |
| Triage publishes | 600–750 | Triage's routing decision is *also* a signed record. |
| Bridge dispatches | 750–900 | The bridge looks up the agent's endpoint, then dispatches over **A2A** — the only non-firehose hop. |
| Weather LLM | 900–1050 | The weather agent answers. It never touches atproto. |

**Bridge registry — the visual touch.** During the *Bridge dispatches* beat, a
small inset rectangle inside the Bridge box briefly highlights, showing a
phone-book row like `@weather-one.test → :5051`. ~30 frames of pulse. No
extra caption needed; the existing caption mentions "looks up the agent's
endpoint," and the visual carries the rest. This is the pedagogical hook for
"the bridge has a registry," without forcing the main animation to explain
where the registry came from. That story belongs in its own piece — see
"Future animations" below.
| Bridge writes result | 1050–1200 | The bridge signs the answer as the weather agent — a record in its repo. |
| Browser shows | 1200–1350 | It fans back through the firehose, like every other record. |

Cross-cutting check: read the right column straight through. It should be a
coherent eleven-sentence summary of the system, with no caption that depends
on hearing the previous one to make sense. If a caption only works as a
follow-up to the one before it, rewrite it. (Friends will pause; friends will
skim back; friends will share a still.)

A note on the intro caption: it's the only one that's a *title card*
(prominent, centered, larger type), not a bottom-of-frame caption. It primes
the scenario before the trace starts. The other ten are bottom-of-frame in
the standard caption style.

**On DID.** The captions don't introduce the term "DID" explicitly — it's
deferred to a hover/footnote in the final piece, or to a one-line prefix the
narrator can add if presenting live. Reason: cramming "DID" into the write
beat ("Carlos's *DID* signs a *record* into his *repo* on the *PDS*") puts
four new terms in one caption, which is too dense. The visual already shows
the handle `carlos.test` on Carlos's box — that's enough to ground identity
without naming the abstraction. If feedback says "but what *is* his identity,
formally?" we add a short follow-up beat or a card before the title intro.

**On the SQLite/Postgres split.** Not mentioned in captions either — same
reason; it's a substrate detail that doesn't change the trace. But worth
keeping in our heads when answering questions after a screening: Postgres is
PLC's database, not the PDS's. The PDS stores records in SQLite per account.
The diagram is honest about this (Postgres hangs off PLC, not PDS) — friends
who notice should be told.

## Project setup

```bash
mkdir -p ~/projects/murmur-animation && cd ~/projects/murmur-animation
npx create-video@latest
# choose: TypeScript, blank starter
npm i           # if create-video doesn't do it
npm run dev     # opens Remotion Studio for live preview
```

Project layout we want:

```
src/
  Root.tsx                    # registers Compositions
  WeatherTrace/
    index.tsx                 # the Composition root
    Diagram.tsx               # static diagram (one big JSX SVG)
    Box.tsx                   # generic box with "at rest" / "active" props
    EdgeDot.tsx               # animated dot traveling along a path
    Caption.tsx               # bottom-of-frame caption with fade
    LLMSpinner.tsx            # small spinner overlay
    timeline.ts               # one place where all the frame numbers live
public/
  (any static assets)
```

Keeping all frame numbers in `timeline.ts` is the difference between "easy to
tweak after first render" and "going to grep for the magic number 750." Worth
the discipline.

## Component sketch

### `Diagram.tsx`

Static layout of every box and edge from the architecture diagram, positioned
in absolute coordinates that match the .drawio file roughly. Each box takes an
`activity: 'rest' | 'active'` prop and applies the corresponding style. Each
edge takes a `phase: 'idle' | 'firing' | 'lit'` prop and renders accordingly.

Don't try to be pixel-perfect to the drawio. The animation is a stylized cousin
of the diagram, not a video of the diagram. Aim for "recognizable as the same
system" — same box layout, same color palette, same labels.

### `EdgeDot.tsx`

```ts
type Props = {
  from: {x: number, y: number},
  to:   {x: number, y: number},
  startFrame: number,
  durationFrames: number,  // ~12
  color: string,           // blue/orange/green/purple/gray per legend
}
```

Internally: `useCurrentFrame()`, compute local frame relative to `startFrame`,
`interpolate(local, [0, durationFrames], [0, 1])` for the t-parameter along
the path, render an SVG `<circle>` at the interpolated point. Add a subtle
trailing path with reduced opacity using the same interpolation but lagged a
few frames.

### `Composition root` — `WeatherTrace/index.tsx`

Reads the global `useCurrentFrame()`. For each box, computes its activity at
this frame from `timeline.ts`. For each edge fire, mounts an `<EdgeDot>` with
a fixed `startFrame`. Captions are `<Sequence from={...} durationInFrames={...}>`
wrapping a `<Caption text="...">`.

Don't try to be clever with state. Every animation is a pure function of the
current frame — Remotion's whole pitch.

### `timeline.ts`

```ts
export const TIMELINE = {
  intro:           { in:    0, out:   90 },
  carlosTypes:     { in:   90, out:  180 },
  cliWrite:        { in:  180, out:  270 },
  pdsToRelay:      { in:  270, out:  360 },
  relayFanout:     { in:  360, out:  450 },
  triageLLM:       { in:  450, out:  600 },
  triagePublish:   { in:  600, out:  750 },
  bridgeDispatch:  { in:  750, out:  900 },
  weatherLLM:      { in:  900, out: 1050 },
  bridgeResult:    { in: 1050, out: 1200 },
  browserUpdate:   { in: 1200, out: 1350 },
};
```

Adjusting a beat is editing one number, not chasing magic numbers through five
files.

## Implementation order

Rough effort estimates assume someone comfortable with React+TS but new to
Remotion. Half a day total for a first watchable version is realistic.

1. **Scaffold + smoke-test render** (30 min). `create-video`, get a "hello
   world" 1080p MP4 rendering to confirm the toolchain works end-to-end.
2. **Static diagram** (90 min). Hand-port the boxes from `architecture.drawio`
   into JSX SVG. Don't animate yet. Render a still frame at frame 0; confirm
   it looks like the diagram.
3. **Box activation** (30 min). `Box.tsx` with `activity` prop, glow + opacity
   transitions via `interpolate`. Test by hardcoding one box active.
4. **Edge dot** (45 min). `EdgeDot.tsx`. Test by firing one edge from CLI →
   Caddy at a known frame.
5. **Timeline wiring** (60 min). `timeline.ts`, then every box and edge driven
   from it. First end-to-end render at this point; it'll be rough but watchable.
6. **Captions** (30 min). `Caption.tsx` + per-beat text in `timeline.ts`.
7. **LLM spinner + polish** (60 min). The two LLM calls (triage and weather)
   need their own visual beat — a spinner near the calling box plus a quick
   highlight of the AgentGateway → WatsonX path.
8. **Iterate on timing** (60 min). Watch the render. Almost every beat will
   feel either too fast or too slow on first viewing. Adjust numbers in
   `timeline.ts` and re-render.

## Rendering

```bash
# preview in Studio (hot-reload)
npm run dev

# render to MP4
npx remotion render src/index.ts WeatherTrace out/weather-trace.mp4

# render a single still for thumbnails
npx remotion still src/index.ts WeatherTrace out/thumb.png --frame=600
```

The MP4 output is portable — drops into Keynote, embeds in the writeup,
uploads to any social/sharing surface.

## Future animations (worth planning, not for this build)

These are sketches, not full plans — but the current scaffolding is reusable,
so each is a "fork the project, swap `timeline.ts` and a few components" cost.

### A. "How a new agent joins the swarm" (~25 seconds)

The bootstrap / discovery / registry story. The right vehicle for explaining
how the bridge *knows* what to dispatch — by showing one being built. Good
for the writeup; explicitly *not* needed for the friends demo.

Storyboard:

1. Empty swarm — PDS, relay, AppView, Bridge all up, but the AppView's
   directory and the Bridge's registry are visibly empty.
2. A new agent boots and publishes its `community.swarm.agent.profile`
   record to its PDS (a single signed record with handle, name,
   description, and `a2aEndpoint`).
3. The relay carries the commit on the firehose.
4. The AppView indexes the profile — its directory now has one entry.
5. The Bridge polls `GET /api/agents` (or sees the live firehose event,
   whichever is cleaner to animate) and adds the row to its registry —
   `@agent → A2A endpoint`.
6. The agent is now part of the swarm. No central registration; no human in
   the loop; in a federated world this same flow announces the agent to
   *every* AppView in the network.

The writeup angle is "agents are atproto records, discovery is the firehose,
no central registry" — federation-readiness in 25 seconds.

### B. Multi-mention fan-out (~20 seconds)

Same starting frame as the main animation, but Carlos posts
`"@weather-one.test @agent-one.test how are you?"`. Triage never activates;
the Bridge fans out in parallel; two answers come back independently. Good
contrast piece — friends watching both immediately get that mention pattern
*is* a routing choice.

### C. Multi-PDS / federation (the flow.md scenario, ~60+ seconds)

The cross-PDS trace from flow.md: three users, two privacy levels, an
orchestrator scrub step, the AGCO query. This is the writeup's hero asset
if it ever gets built. Significantly more complex — three stacks visible
side by side, color-coded private vs. public PDSes, the scrub step
highlighted as the security-critical hop. Plan separately when the writeup
needs it.

## Out of scope for v1

Listing these so we don't get tempted:

- **Voiceover.** Captions are enough. Adding narration means a recording step
  and a sync pass.
- **Multi-PDS / federation animation.** The flow.md scenario deserves its own
  animation later. Same scaffolding, different storyboard.
- **Multi-mention fan-out animation.** Good demo of "bridge skips triage";
  ~20 second variant we can build by forking this project.
- **Anything that animates real LLM token streams.** The point is the routing,
  not the prose.
- **Real-time generation from logs.** Would be cool eventually — replay an
  actual demo session as an animation by feeding it the firehose log. Way out
  of scope for now.

## Open questions to settle before writing code

1. **Backdrop:** stay close to the architecture.drawio look, or restyle for
   video (darker background, brighter highlights, less text per box)? My vote:
   restyle slightly for video — diagrams are read, videos are watched.
2. **Carlos as visible character:** show a stylized terminal/browser as the
   entry point, or just label the incoming arrow "user posts"? Trade-off:
   character grounds the viewer but adds an asset to build.
3. **End frame:** loop or stop? Stop with the answer visible reads more like
   "demo finished." Loop reads more like "this is how the system runs." For
   embed-in-slide use, stop is better.

Decide these three before opening the editor.
