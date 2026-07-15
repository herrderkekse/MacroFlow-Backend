import { useState, type FormEvent } from "react";

import { deleteAccount, setPassword, wipeData, type AdminUser } from "./api.ts";
import { formatBytes } from "./format.ts";

export type DialogKind = "password" | "wipe" | "delete";

export interface DialogState {
  kind: DialogKind;
  user: AdminUser;
}

interface Props {
  dialog: DialogState;
  onClose: () => void;
  onDone: () => void; // called after a successful action, to refresh
}

// ActionDialog renders the confirm dialog for the three user actions. The two
// destructive ones make the blast radius explicit; deleting an account
// additionally requires typing the username.
export function ActionDialog({ dialog, onClose, onDone }: Props) {
  const { kind, user } = dialog;
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (kind === "password") await setPassword(user.username, input);
      else if (kind === "wipe") await wipeData(user.username);
      else await deleteAccount(user.username);
      onDone();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const confirmDisabled =
    busy ||
    (kind === "password" && input.length < 8) ||
    (kind === "delete" && input !== user.username);

  return (
    <div className="overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        {kind === "password" && (
          <>
            <h3>Reset password — {user.username}</h3>
            <p>
              Set a new password for this account. Their devices will need to
              sign in again with it.
            </p>
          </>
        )}
        {kind === "wipe" && (
          <>
            <h3>Wipe data — {user.username}</h3>
            <p>
              Permanently deletes this user's stored change log (
              {user.rows.toLocaleString("en-US")} rows,{" "}
              {formatBytes(user.bytes)}). The account itself is kept, and their
              devices will re-upload local data on the next sync.
            </p>
          </>
        )}
        {kind === "delete" && (
          <>
            <h3>Delete account — {user.username}</h3>
            <p>
              Permanently deletes this account <em>and</em> all its stored data
              ({user.rows.toLocaleString("en-US")} rows,{" "}
              {formatBytes(user.bytes)}). Type the username to confirm.
            </p>
          </>
        )}

        <form onSubmit={run}>
          {kind === "password" && (
            <input
              type="password"
              placeholder="New password (min 8 characters)"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              autoFocus
            />
          )}
          {kind === "delete" && (
            <input
              type="text"
              placeholder={user.username}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              autoFocus
            />
          )}
          {error && <div className="error">{error}</div>}
          <div className="buttons">
            <button type="button" onClick={onClose} disabled={busy}>
              Cancel
            </button>
            <button
              type="submit"
              className={kind === "password" ? "primary" : "danger"}
              disabled={confirmDisabled}
            >
              {kind === "password" && (busy ? "Saving…" : "Set password")}
              {kind === "wipe" && (busy ? "Wiping…" : "Wipe data")}
              {kind === "delete" && (busy ? "Deleting…" : "Delete account")}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
