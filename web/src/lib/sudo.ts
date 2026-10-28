// Sudo mode: sensitive actions answer 403 SUDO_REQUIRED when the session
// wasn't confirmed in the last 10 minutes (application/two_factor.go).
// api() then asks the registered handler (SudoDialog, in the layout) to
// confirm, and retries once.

type Handler = () => Promise<boolean>;

let handler: Handler | null = null;

export function setSudoHandler(h: Handler | null) {
  handler = h;
}

// confirmSudo resolves true once the user confirmed; false if they
// cancelled or nothing can ask them.
export function confirmSudo(): Promise<boolean> {
  return handler ? handler() : Promise.resolve(false);
}
