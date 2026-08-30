# AI Gateway Interface Direction

## Product

AI Gateway is a private AI workbench. Creators choose a workflow, prepare media
and prompts, launch a job, inspect its output and return to it later. Operators
manage users, invitations, services, media and system state.

The interface must feel like a reliable creative tool, not a landing page and
not a raw ComfyUI graph editor.

## Visual Direction

**Quiet technical workbench.** Dense enough for repeated work, with restrained
contrast, clear state colors and one consistent spacing rhythm. The interface
uses dark neutral surfaces, mint only for committed actions and active states,
cyan for informational focus, amber for warnings and coral for destructive or
failed states.

## Layout Rules

- A page is a sequence of full-width working sections, not a stack of nested
  cards.
- A panel frames a complete task or repeated data collection. Do not place
  decorative cards inside a panel.
- Forms group fields by the decision they support. Conditional fields stay
  hidden until the controlling option is enabled.
- Desktop grids use content-sized `minmax()` tracks. Mobile collapses to one
  task column before labels or controls become cramped.
- Repeated generation cards use three columns on a wide workspace, two on a
  tablet and one on a phone. Never leave a single narrow card stranded in a
  new row because of an automatic column count.
- Workflow settings use deliberate groups: core values, optional processing
  and export. Do not mix numeric fields and binary utility switches in one
  undifferentiated grid.
- Tables are for scanning and selection. Editing remains in a detail page or a
  focused form area.

## Type And Copy

- Manrope is the UI font. Use three roles only: page title, section title and
  utility label.
- Primary labels describe the user result: "Плавность движения", not "RIFE";
  the technical name can be a secondary tag.
- Keep implementation details, node names and raw values inside tooltips or
  expandable diagnostics unless the page is explicitly for administrators.

## Controls And States

- Use a checkbox or switch for a binary choice, a select for a finite option
  set, and an input only when free numeric entry is useful.
- Primary action is mint. Secondary action is neutral. Destructive action is
  coral and always has a clear verb.
- Every async state needs a visible running, completed and error state. Errors
  state the consequence and the next safe action.
- Keyboard focus must be visible; interactive targets on a touch layout must
  be at least 44 px high.

## Visual QA

Before shipping a UI change, inspect the changed route at a wide desktop
viewport (1440 px or wider) and a narrow mobile viewport (390 px). Check the
default, selected, disabled, loading, empty and error states that exist for the
surface. Verify that long names, prompts and filenames neither overlap nor
silently hide the primary action.
