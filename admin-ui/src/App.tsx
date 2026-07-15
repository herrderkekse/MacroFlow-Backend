import { useCallback, useEffect, useState, type ReactNode } from "react";

import {
  fetchOverview,
  fetchUsers,
  type AdminUser,
  type Overview,
} from "./api.ts";
import { ActionDialog, type DialogState } from "./dialogs.tsx";
import {
  formatBytes,
  formatCount,
  formatUptime,
  formatWhen,
} from "./format.ts";

const REFRESH_MS = 10_000;

export default function App() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [users, setUsers] = useState<AdminUser[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [dialog, setDialog] = useState<DialogState | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [ov, us] = await Promise.all([fetchOverview(), fetchUsers()]);
      setOverview(ov);
      setUsers(us);
      setError(null);
      setUpdatedAt(Date.now());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = setInterval(() => void refresh(), REFRESH_MS);
    return () => clearInterval(timer);
  }, [refresh]);

  return (
    <div className="container">
      <div className="header">
        <h1>MacroFlow Admin</h1>
        <span className="spacer" />
        {updatedAt && (
          <span className="updated">updated {formatWhen(updatedAt)}</span>
        )}
        <button onClick={() => void refresh()}>Refresh</button>
      </div>

      {error && <div className="banner">Cannot reach the server: {error}</div>}

      {overview && <Tiles overview={overview} />}
      {users && overview && (
        <UsersTable
          users={users}
          quota={overview.config.maxUserBytes}
          onAction={setDialog}
        />
      )}

      {dialog && (
        <ActionDialog
          dialog={dialog}
          onClose={() => setDialog(null)}
          onDone={() => void refresh()}
        />
      )}
    </div>
  );
}

function Tiles({ overview: ov }: { overview: Overview }) {
  const errs = ov.requests.serverErrors;
  return (
    <div className="tiles">
      <StatTile
        label="Storage used"
        value={formatBytes(ov.storage.bytes)}
        sub={`${formatCount(ov.storage.rows)} rows · ${formatBytes(ov.storage.dbFileBytes)} on disk`}
      />
      <StatTile
        label="Users"
        value={String(ov.users.static + ov.users.accounts)}
        sub={`${ov.users.accounts} accounts · ${ov.users.static} static · signup ${ov.config.signupAllowed ? "on" : "off"}`}
      />
      <StatTile
        label="Requests since start"
        value={formatCount(ov.requests.total)}
        sub={
          <>
            <span
              className="status-dot"
              style={{ background: errs === 0 ? "var(--good)" : "var(--critical)" }}
            />
            {errs === 0
              ? `no server errors · ${formatCount(ov.requests.clientErrors)} client errors`
              : `${formatCount(errs)} server errors · ${formatCount(ov.requests.clientErrors)} client errors`}
          </>
        }
      />
      <StatTile
        label="Uptime"
        value={formatUptime(ov.uptimeSeconds)}
        sub={`since ${new Date(ov.startedAt).toLocaleString()} · ${ov.goVersion}`}
      />
    </div>
  );
}

function StatTile({
  label,
  value,
  sub,
}: {
  label: string;
  value: string;
  sub: ReactNode;
}) {
  return (
    <div className="tile">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      <div className="sub">{sub}</div>
    </div>
  );
}

function UsersTable({
  users,
  quota,
  onAction,
}: {
  users: AdminUser[];
  quota: number;
  onAction: (d: DialogState) => void;
}) {
  return (
    <div className="card">
      <h2>Users</h2>
      <div className="table-scroll">
        <table>
          <thead>
            <tr>
              <th>User</th>
              <th className="num">Devices</th>
              <th className="num">Rows</th>
              <th className="num">Storage</th>
              {quota > 0 && <th>Quota</th>}
              <th>Last change</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.username}>
                <td>
                  {u.username}{" "}
                  <span className={`pill ${u.source === "orphaned" ? "orphaned" : ""}`}>
                    {u.source === "orphaned" ? "⚠ orphaned" : u.source}
                  </span>
                </td>
                <td className="num">{u.devices}</td>
                <td className="num">{u.rows.toLocaleString("en-US")}</td>
                <td className="num">{formatBytes(u.bytes)}</td>
                {quota > 0 && (
                  <td>
                    <QuotaMeter bytes={u.bytes} quota={quota} />
                  </td>
                )}
                <td className="muted">{formatWhen(u.lastChangeAt)}</td>
                <td>
                  <div className="actions">
                    {u.source === "account" && (
                      <button onClick={() => onAction({ kind: "password", user: u })}>
                        Reset password
                      </button>
                    )}
                    <button
                      className="danger"
                      disabled={u.rows === 0}
                      onClick={() => onAction({ kind: "wipe", user: u })}
                    >
                      Wipe data
                    </button>
                    {u.source === "account" && (
                      <button
                        className="danger"
                        onClick={() => onAction({ kind: "delete", user: u })}
                      >
                        Delete
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            ))}
            {users.length === 0 && (
              <tr>
                <td colSpan={quota > 0 ? 7 : 6} className="empty">
                  No users yet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// QuotaMeter: the fill carries severity (accent → warning → critical as the
// cap nears); the % text beside it carries the value, so color is never the
// only channel.
function QuotaMeter({ bytes, quota }: { bytes: number; quota: number }) {
  const ratio = Math.min(bytes / quota, 1);
  const cls = ratio >= 0.9 ? "critical" : ratio >= 0.75 ? "warning" : "";
  return (
    <div className="meter-cell">
      <div className="meter">
        <div className={cls} style={{ width: `${ratio * 100}%` }} />
      </div>
      <span className="pct">{Math.round(ratio * 100)}%</span>
    </div>
  );
}
