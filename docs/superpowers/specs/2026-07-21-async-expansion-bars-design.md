# Async Expansion Bars — Design

Date: 2026-07-21
Status: Approved by user

## Goal

Submitting a text expansion must not interrupt reading. A reader can submit
multiple expansions, keep scrolling, and open each generated expansion from a
fixed bar once it is ready.

## User experience

- On submit, the selection popover closes immediately. The page does not
  reload.
- A fixed container spans the full viewport width at the top of the screen.
  Each in-flight request gets a separate stacked bar in that container.
- A pending bar says `Expanding text “<selection>”`. Its text is constrained
  with CSS (`overflow: hidden`, `text-overflow: ellipsis`, and `white-space:
  nowrap`), not truncated in JavaScript.
- When generation succeeds, the same bar becomes a link to the generated
  expansion. The source document is already rewritten on the server, but the
  current page remains untouched until the reader chooses to reload it.
- When generation fails, the bar remains in place and shows the server error.
  There is no retry control.
- Every bar has an accessible `×` control that removes only that bar.
- Bars and client tracking last only for the open page. Refreshing or leaving
  the page clears them; no historical job UI is restored.

## Architecture

### Server: async, pollable expansion jobs

`POST /expansions` validates the request and creates an expansion job rather
than running `ClaudeExpandService` in the request. It responds immediately
with the job identifier and a pending state.

The job performs the existing sequence in the background: resolve the source,
link the selection, generate the HTML, write the generated file, and rewrite
the source. Its terminal state is either:

- `completed`, with `url` for the generated page; or
- `failed`, with the existing safe, user-facing detail.

`GET /expansions/:id` returns the current state. It is authorized to the same
signed-in user who created the job and returns only that job's status, URL, or
safe failure detail. Polling a missing or inaccessible job returns `404`.

Jobs are persisted server-side so an active background worker and the status
endpoint can coordinate safely, but no page restoration behavior is added.
Completed/failed job cleanup is separate operational maintenance and not part
of this feature.

### Client: independent bars and polling

`expand.js` keeps a page-local map of submitted job IDs. On each successful
create response it adds a bar, dismisses the popover, and starts polling that
job's status endpoint at a modest fixed interval. Each job has independent
polling, completion, failure, and dismissal handling, so several expansions
can run at once.

Polling stops for an individual bar when it reaches a terminal state or is
dismissed. A network/polling error is rendered as a dismissible error in that
bar; it never reloads the page or blocks new submissions. Request creation
errors remain in the form popover because no job exists yet.

The container uses `position: fixed; top: 0; left: 0; width: 100%;` with a
high stacking level. Bars are laid out vertically and use flexbox so the text
area is the only shrinking element while the close control stays visible.

## Data flow

1. Reader submits an expansion form.
2. Browser posts the selection, occurrence, question, and provider choice.
3. Server creates a job and returns its ID without waiting for AI generation.
4. Browser displays a full-width pending bar and resumes normal reading.
5. Browser polls that job until it reports `completed` or `failed`.
6. On completion, the row turns into its generated-page link. On failure, it
   displays the error. In either state, `×` removes the row.

## Error handling and integrity

- Existing parameter, file-path, and selection-linking validation remain in
  the job flow and result in `failed` jobs with safe details.
- Generation failures continue to be logged server-side while exposing only
  `Generation failed.` to the reader.
- The source file is rewritten only after successful generation, preserving
  the existing all-or-nothing behavior.
- The job must protect against duplicate execution/status races so a job
  cannot write the source or generated file twice.

## Testing

- Controller/model tests cover creation, user-scoped status lookup, pending,
  completed, and failed payloads, including missing/unauthorized IDs.
- Job tests cover success (file write and source linking), validation failure,
  generator failure, and exactly-once terminal-state updates.
- Front-end tests, if lightweight browser/JS coverage is available, verify
  independent bars, fixed container styles, CSS ellipsis properties, terminal
  transitions, dismissal, and polling cancellation. Otherwise those cases are
  covered by focused manual verification alongside the Rails suite.

## Out of scope

- Persisting bars across refreshes, tabs, or navigation.
- Retrying failed expansions from the bar.
- Live push transport such as WebSockets or SSE.
- Editing or deleting generated expansion pages.
