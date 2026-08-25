# Review: T8 - Create systemd service unit

## Changes Made
None found.

## Files Touched
None.

## Test Results
N/A — feature does not exist.

## Concerns / Trade-offs
No `.service` file, `featherdesk.service`, or any systemd-related file exists
anywhere in the repository (checked across all branches via
`git ls-tree -r --name-only | grep -i service`). None of the plan's
sub-items — the unit file itself, setcap documentation, a start/stop/restart
lifecycle test, or the `After=pipewire.service` dependency — were built.

## Verdict
FAIL
