'use client';

// The cm-beetle browser. Rows are set aside into the shortlist above it, which
// spells out the exact reference each one produces, so a wrong node is obvious
// before it reaches a plan.
//
// Browsing only. Choosing a destination happens in the wizard's Step 2, which
// renders the shortlist on its own — so nothing here takes an onUse.
//
// The three lists are alternatives to one another, which is the one case where
// this UI uses tabs instead of stacking.

import React from 'react';
import { useBeetleInfra, useBeetleObjectStorages, useBeetleRDBMS, useCheckSSHReady } from '@/queries/beetle';
import {
  Badge,
  Button,
  ErrorNote,
  Loading,
  Note,
  Panel,
  RefreshButton,
  TextInput,
} from '@/components/ui/primitives';
import { cx } from '@/lib/format';
import { TargetShortlist } from './TargetShortlist';
import { useTargetShortlist, type TargetSelection } from '@/store/targetShortlistStore';
import type { TargetKind } from '@/lib/targetPresence';
import { targetLabel } from '@/lib/targetLabel';

export type { TargetSelection };

/**
 * The same three kinds `TargetKind` names, under the word this component uses
 * for them. An alias rather than a second union: `adapter.targetTabs` is read
 * both as this component's tab set and as the kinds a destination may be, and
 * two literal unions would eventually disagree about one of them.
 */
export type TargetTab = TargetKind;

const TABS: Array<{ id: TargetTab; label: string }> = [
  { id: 'infra', label: 'VM Nodes' },
  { id: 'objectStorage', label: 'Object Storage' },
  { id: 'rdbms', label: 'RDBMS' },
];

export function TargetPicker({
  nsId,
  onNsChange,
  tabs = TABS.map((t) => t.id),
}: {
  nsId: string;
  onNsChange: (ns: string) => void;
  /** Restrict the offered kinds. Every kind is offered when omitted. */
  tabs?: TargetTab[];
}) {
  const visibleTabs = TABS.filter((t) => tabs.includes(t.id));
  const [tab, setTab] = React.useState<TargetTab>(visibleTabs[0]?.id ?? 'infra');

  // One selection used to be held here and shared by all three tabs, which let a
  // node picked under "VM Nodes" be committed while the reader was looking at
  // "Object Storage". Rows now go to a named list instead, where what is about
  // to be used is written down.
  const shortlisted = useTargetShortlist((s) => s.items);
  const addToShortlist = useTargetShortlist((s) => s.add);
  const isShortlisted = (label: string) => shortlisted.some((i) => i.label === label);

  const infra = useBeetleInfra(nsId, tab === 'infra');
  const os = useBeetleObjectStorages(nsId, tab === 'objectStorage');
  const rdbms = useBeetleRDBMS(nsId, tab === 'rdbms');
  const sshReady = useCheckSSHReady();

  const active = tab === 'infra' ? infra : tab === 'objectStorage' ? os : rdbms;

  return (
    <Panel>
      {/* Above the browser, not below it: it survives the namespace box under
          it, so a reader sees what they have gathered while gathering more. No
          onUse — this screen has no destination to set. */}
      <div className="mb-4">
        <TargetShortlist />
      </div>

      <div className="flex flex-wrap items-end gap-3 mb-4">
        <label className="text-xs font-mono text-text-muted">
          <span className="block mb-1 font-bold">Namespace</span>
          <TextInput
            value={nsId}
            onChange={(e) => onNsChange(e.target.value)}
            className="w-40"
          />
        </label>
        {/* Bound to the active tab's own query, so the spinner reports the list
            actually being refetched rather than all three. */}
        <RefreshButton
          onClick={() => active.refetch()}
          busy={active.isFetching}
          title="Reload this list from cm-beetle"
        />
      </div>

      <div className="flex gap-2 border-b border-border-main mb-4">
        {visibleTabs.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={cx(
              'px-3 py-2 text-xs font-bold cursor-pointer border-b-2 -mb-px transition',
              tab === t.id
                ? 'border-indigo-500 text-indigo-600 dark:text-indigo-400'
                : 'border-transparent text-text-muted hover:text-text-main',
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      <ErrorNote error={active.error} />

      {active.isPending ? (
        <Loading />
      ) : tab === 'infra' ? (
        <div className="space-y-4">
          {(infra.data?.infra ?? []).map((inf) => (
            <div key={inf.id}>
              <div className="flex items-center gap-2 mb-1.5">
                <span className="text-xs font-mono font-bold text-text-muted">
                  infra: {inf.id}
                </span>
                <Badge>{inf.status}</Badge>
                <Button
                  onClick={() => sshReady.mutate({ nsId, infraId: inf.id })}
                  disabled={sshReady.isPending}
                  title="Opens real SSH connections — run it only when you need it"
                >
                  {sshReady.isPending ? 'checking…' : 'check ssh-ready'}
                </Button>
              </div>
              <ul className="space-y-1">
                {(inf.node ?? []).map((n) => {
                  const sel: TargetSelection = {
                    ref: {
                      source: 'beetleSsh',
                      beetleSsh: { nsId, infraId: inf.id, nodeId: n.id },
                    },
                    label: targetLabel('beetleSsh', `${nsId} / ${inf.id} / ${n.id}`),
                  };
                  return (
                    <li key={n.id}>
                      <PickRow added={isShortlisted(sel.label)} onAdd={() => addToShortlist(sel)}>
                        <span className="font-bold">{n.id}</span>
                        <span className="text-text-muted">
                          {n.publicIP}:{n.sshPort}
                        </span>
                        <span className="text-text-muted">{n.nodeUserName}</span>
                      </PickRow>
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
          {(infra.data?.infra ?? []).length === 0 && (
            <p className="text-xs text-text-muted font-mono">
              No migrated infrastructure in namespace “{nsId}”.
            </p>
          )}
        </div>
      ) : tab === 'objectStorage' ? (
        <ul className="space-y-1">
          {(os.data?.objectStorage ?? []).map((b) => {
            const sel: TargetSelection = {
              ref: { source: 'beetleObjectStorage', beetleObjectStorage: { nsId, osId: b.id } },
              label: targetLabel('beetleObjectStorage', `${nsId} / ${b.id}`),
            };
            return (
              <li key={b.id}>
                <PickRow added={isShortlisted(sel.label)} onAdd={() => addToShortlist(sel)}>
                  <span className="font-bold">{b.id}</span>
                  <Badge>{b.status}</Badge>
                  {b.connectionName && <span className="text-text-muted">{b.connectionName}</span>}
                </PickRow>
              </li>
            );
          })}
          {(os.data?.objectStorage ?? []).length === 0 && (
            <p className="text-xs text-text-muted font-mono">
              No object storage in namespace “{nsId}”.
            </p>
          )}
        </ul>
      ) : (
        <div>
          <ul className="space-y-1">
            {(rdbms.data?.rdbms ?? []).map((r) => {
              const sel: TargetSelection = {
                ref: {
                  source: 'beetleDb',
                  // The password is not collected here: cm-beetle never returns
                  // one, so there is nowhere to keep it. The wizard asks for it
                  // at Step 2 every time.
                  beetleDb: { nsId, rdbmsId: r.id, password: '' },
                },
                label: targetLabel('beetleDb', `${nsId} / ${r.id}`),
              };
              return (
                <li key={r.id}>
                  <PickRow added={isShortlisted(sel.label)} onAdd={() => addToShortlist(sel)}>
                    <span className="font-bold">{r.id}</span>
                    <span className="text-text-muted">{r.dbEngine}</span>
                    <Badge tone={r.status === 'Available' ? 'ok' : 'warn'}>{r.status}</Badge>
                    <span className="text-text-muted">{r.endpoint ?? '-'}</span>
                    <span className="text-text-muted">admin: {r.adminUserName}</span>
                    <Badge>{r.publicAccess ? 'public' : 'private'}</Badge>
                  </PickRow>
                </li>
              );
            })}
          </ul>
          {(rdbms.data?.rdbms ?? []).length === 0 && (
            <p className="text-xs text-text-muted font-mono">
              No managed RDBMS in namespace “{nsId}”.
            </p>
          )}
          <div className="mt-3">
            <Note>
              The admin password is never returned by cm-beetle. You will be asked for it in the
              wizard (Step 2).
            </Note>
          </div>
        </div>
      )}
    </Panel>
  );
}

/**
 * One browsable target. The button is the only way to take it: a radio here used
 * to mean "the one", but a row is now set aside rather than chosen, and several
 * can be.
 */
function PickRow({
  added,
  onAdd,
  children,
}: {
  added: boolean;
  onAdd: () => void;
  children: React.ReactNode;
}) {
  return (
    <div
      className={cx(
        'flex flex-wrap items-center gap-3 text-xs font-mono px-3 py-2 rounded-lg border transition',
        added ? 'border-indigo-500 bg-indigo-500/5' : 'border-border-main',
      )}
    >
      {children}
      <Button
        className="ml-auto"
        onClick={onAdd}
        disabled={added}
        title={added ? 'Already in the list above' : 'Set this aside in the list above'}
      >
        {added ? 'selected' : '+ select'}
      </Button>
    </div>
  );
}
