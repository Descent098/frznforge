---
title: Heat buckets — reading a repo's age as temperature
description: Why frznforge colours things fire-to-ice by age, where the five boundaries came from, and why the clock is an argument.
date: 2026-06-02
tags:
  - design
  - frznforge
  - css
---

# Heat buckets — reading a repo's age as temperature

A read-only forge has no stars and no issue count, so the listing needs some other signal for
"is this alive?". frznforge uses temperature: recent work is fire, old work is ice. It reads
at a glance and it degrades gracefully — a colour-blind visitor still gets the date text and
the ordering.

## The five buckets

| Bucket | Age of last commit | Reads as |
| --- | --- | --- |
| `hot` | under 7 days | ember gradient |
| `warm` | under 30 days | amber |
| `neutral` | under 180 days | plain rule |
| `cool` | under 365 days | pale ice |
| `cold` | 365 days or more, or no date | ice |

Five, not a continuous ramp, and the boundaries are the ones a person already thinks in:
this week, this month, this half-year, this year, before that. A gradient would be prettier
and would say less — you cannot tell 40% blue from 55% blue, but you can tell "warm" from
"cold" across a page of cards.

## The clock is an argument

```ts
export function heatFor(date: string | Date | null | undefined, now: Date = new Date()): Heat {
  if (!date) return 'cold';
  const age = now.getTime() - new Date(date).getTime();
  if (age < 7 * DAY) return 'hot';
  if (age < 30 * DAY) return 'warm';
  if (age < 180 * DAY) return 'neutral';
  if (age < 365 * DAY) return 'cool';
  return 'cold';
}
```

`now` being a parameter is the whole trick. Tests pin it, and nothing in the ingest artifact
ever stores a bucket — heat is computed at render time from a stored date. If the bucket were
baked into `forge.json`, the same inputs would produce different bytes tomorrow, and
determinism is the one property this project will not trade away.

## Wiring it up

The function returns a bucket name; a second helper turns it into a class, and CSS does the
rest. No inline styles, no colour values in TypeScript:

- `heatFor(date)` → `'warm'`
- `heatClass('warm')` → `'t-warm'`
- `.hf-repo-card.heat-warm` sets `--heat` and `--heat-soft`, the card paints its top rule and
  its glow from those two custom properties.

That indirection is what lets the whole palette swap between `hearth` and `frost` from one
config key: the buckets stay put, the tokens underneath them change. Same layout, same
components, different weather.
