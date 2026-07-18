# Termada Operational Frame

## Canvas

- `canvas`: `#0b0f14`
- `panel`: `#111820`
- `ink`: `#eef2f6`
- `muted`: `#93a1af`
- `verified`: `#3ecf8e`
- `approval`: `#f2b84b`
- `evidence`: `#5ed4d0`

## Type

- Display: Space Grotesk, weight 800, 72-118px.
- Body: Space Grotesk, weight 350-500, 28-38px.
- Evidence: IBM Plex Mono, weight 500, 24-32px, tabular numbers.

## Layout

- 1920x1080 with 96px outer safe area.
- Real product captures remain at least 70% of the frame.
- Callouts sit in dedicated edge rails and never cover approvals or evidence.
- Use hairline dividers and small uppercase operational labels for hierarchy.

## Motion

- Deterministic push/focus transitions in 0.45 seconds.
- Slow screenshot pan or scale on a nested image only.
- Entrance order follows the evidence hierarchy.
- No decorative effects unrelated to state or proof.
